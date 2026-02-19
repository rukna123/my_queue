// Service partition-controller watches the mqreader StatefulSet and
// redistributes partition ranges across pods when the replica count changes.
// It updates the mqreader-partitions ConfigMap and triggers a rolling restart.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	namespace := getEnv("NAMESPACE", "default")
	ssName := getEnv("STATEFULSET_NAME", "prompted-mqreader")
	cmName := getEnv("CONFIGMAP_NAME", "prompted-mqreader-partitions")
	totalPartitions := getEnvInt("TOTAL_PARTITIONS", 256)
	port := getEnv("PORT", "8090")
	podName := getEnv("POD_NAME", mustHostname())

	cfg, err := rest.InClusterConfig()
	if err != nil {
		slog.Info("in-cluster config unavailable, trying KUBECONFIG", "error", err)
		cfg, err = clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
		if err != nil {
			slog.Error("cannot build k8s config", "error", err)
			os.Exit(1)
		}
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		slog.Error("failed to create k8s clientset", "error", err)
		os.Exit(1)
	}

	// Health endpoint.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		slog.Info("healthz server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("healthz server error", "error", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
		_ = srv.Shutdown(context.Background())
	}()

	reconciler := &partitionReconciler{
		clientset:       clientset,
		namespace:       namespace,
		ssName:          ssName,
		cmName:          cmName,
		totalPartitions: totalPartitions,
		lastReplicas:    -1,
	}

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      "partition-controller-leader",
			Namespace: namespace,
		},
		Client: clientset.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: podName,
		},
	}

	slog.Info("starting leader election", "identity", podName, "namespace", namespace)

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				slog.Info("acquired leadership, starting reconcile loop")
				reconciler.run(ctx)
			},
			OnStoppedLeading: func() {
				slog.Info("lost leadership")
			},
			OnNewLeader: func(identity string) {
				slog.Info("current leader", "identity", identity)
			},
		},
	})

	slog.Info("partition-controller stopped")
}

// ---------------------------------------------------------------------------
// Reconciler
// ---------------------------------------------------------------------------

type partitionReconciler struct {
	clientset       kubernetes.Interface
	namespace       string
	ssName          string
	cmName          string
	totalPartitions int
	lastReplicas    int32
}

func (r *partitionReconciler) run(ctx context.Context) {
	// Reconcile immediately on startup.
	r.reconcile(ctx)

	factory := informers.NewSharedInformerFactoryWithOptions(
		r.clientset, 30*time.Second,
		informers.WithNamespace(r.namespace),
	)

	ssInformer := factory.Apps().V1().StatefulSets().Informer()
	ssInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, newObj interface{}) {
			ss, ok := newObj.(*appsv1.StatefulSet)
			if !ok || ss.Name != r.ssName {
				return
			}
			r.reconcile(ctx)
		},
	})

	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())

	slog.Info("informer watching for StatefulSet changes",
		"statefulset", r.ssName,
		"namespace", r.namespace,
	)

	<-ctx.Done()
}

func (r *partitionReconciler) reconcile(ctx context.Context) {
	ss, err := r.clientset.AppsV1().StatefulSets(r.namespace).Get(
		ctx, r.ssName, metav1.GetOptions{},
	)
	if err != nil {
		slog.Error("failed to get StatefulSet", "name", r.ssName, "error", err)
		return
	}

	replicas := int32(1)
	if ss.Spec.Replicas != nil {
		replicas = *ss.Spec.Replicas
	}

	if replicas == r.lastReplicas {
		return
	}

	slog.Info("replica count changed, recomputing partitions",
		"old", r.lastReplicas,
		"new", replicas,
	)

	partitionsYAML := computePartitionsYAML(r.ssName, int(replicas), r.totalPartitions)

	cm, err := r.clientset.CoreV1().ConfigMaps(r.namespace).Get(
		ctx, r.cmName, metav1.GetOptions{},
	)
	if err != nil {
		slog.Error("failed to get ConfigMap", "name", r.cmName, "error", err)
		return
	}

	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data["partitions.yaml"] = partitionsYAML

	_, err = r.clientset.CoreV1().ConfigMaps(r.namespace).Update(
		ctx, cm, metav1.UpdateOptions{},
	)
	if err != nil {
		slog.Error("failed to update ConfigMap", "name", r.cmName, "error", err)
		return
	}

	slog.Info("updated partition ConfigMap", "configmap", r.cmName, "replicas", replicas)

	// Trigger rolling restart by patching the pod template annotation.
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"prompted.io/restartedAt":"%s"}}}}}`,
		time.Now().Format(time.RFC3339),
	)

	_, err = r.clientset.AppsV1().StatefulSets(r.namespace).Patch(
		ctx, r.ssName, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		slog.Error("failed to patch StatefulSet for restart", "name", r.ssName, "error", err)
		return
	}

	slog.Info("triggered rolling restart", "statefulset", r.ssName)
	r.lastReplicas = replicas
}

// computePartitionsYAML produces the YAML content for the partition ConfigMap.
// Example for ssName="prompted-mqreader", replicas=3, total=256:
//
//	prompted-mqreader-0: "0-84"
//	prompted-mqreader-1: "85-169"
//	prompted-mqreader-2: "170-255"
func computePartitionsYAML(ssName string, replicas, total int) string {
	if replicas <= 0 {
		replicas = 1
	}
	var sb strings.Builder
	perPod := total / replicas
	for i := 0; i < replicas; i++ {
		start := i * perPod
		end := (i+1)*perPod - 1
		if i == replicas-1 {
			end = total - 1
		}
		fmt.Fprintf(&sb, "%s-%d: \"%d-%d\"\n", ssName, i, start, end)
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Env helpers
// ---------------------------------------------------------------------------

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func mustHostname() string {
	h, _ := os.Hostname()
	return h
}
