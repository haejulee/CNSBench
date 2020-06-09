package benchmarktesting

import (
	"context"
	"reflect"
	"strconv"
	"time"
	"fmt"
	"strings"
	"bytes"
	"io"
	"io/ioutil"
	"bufio"
	"os"
	"encoding/json"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/push"
	"github.com/prometheus/client_golang/prometheus"

	benchmarkingv1alpha1 "github.com/benchmark-testing/benchmarktesting-operator/pkg/apis/benchmarking/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	batchv1 "k8s.io/api/batch/v1"
	snapshotcrd "github.com/kubernetes-csi/external-snapshotter/pkg/apis/volumesnapshot/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	//"k8s.io/client-go/rest"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var log = logf.Log.WithName("controller_benchmarktesting")

// Add creates a new BenchmarkTesting Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	scheme := mgr.GetScheme()
	snapshotcrd.AddToScheme(scheme)
	return &ReconcileBenchmarkTesting{client: mgr.GetClient(), scheme: scheme}
	//return &ReconcileBenchmarkTesting{client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("benchmarktesting-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource BenchmarkTesting
	err = c.Watch(&source.Kind{Type: &benchmarkingv1alpha1.BenchmarkTesting{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &batchv1.Job{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &benchmarkingv1alpha1.BenchmarkTesting{},
	})
	if err != nil {
		return err
	}
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &benchmarkingv1alpha1.BenchmarkTesting{},
	})
	if err != nil {
		return err
	}

	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func remove(list []string, s string) []string {
	for i, v := range list {
		if v == s {
			list = append(list[:i], list[i+1:]...)
		}
	}
	return list
}

// blank assignment to verify that ReconcileBenchmarkTesting implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileBenchmarkTesting{}

// ReconcileBenchmarkTesting reconciles a BenchmarkTesting object
type ReconcileBenchmarkTesting struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
}

type ScriptConfig struct {
	cmName string
	scriptName string
	cmdline string
}

type Result struct {
	Config *benchmarkingv1alpha1.BenchmarkTesting
	Results map[string]float64
}

func (r *ReconcileBenchmarkTesting) GetCommandMap(cmName string, namespace string) (ScriptConfig, error) {
	cm := &corev1.ConfigMap{}
	err := r.client.Get(context.TODO(), types.NamespacedName{Name: cmName, Namespace: namespace}, cm)
	if err != nil {
		return ScriptConfig{}, err
	}
	keySet := false
	var key string
	for k := range cm.Data {
		if keySet {
			log.Error(nil, "Only one file allowed")
			return ScriptConfig{}, fmt.Errorf("Too many files")
		}
		key = k
		keySet = true
	}
	return ScriptConfig {
		cmName: cmName,
		scriptName: key,
	}, nil
}

//func newParserPod(iouPod corev1.Pod) *corev1.Pod {
//}

/*
func (r *ReconcileBenchmarkTesting) RunParserPod(instance *benchmarkingv1alpha1.BenchmarkTesting, iou benchmarkingv1alpha1.IoOpUnit, iouPod corev1.Pod) error {
	parserConfig, err := r.GetCommandMap(iou.OutputParser, instance.Namespace)
	if err != nil {
		return err
	}
	parserConfig.cmdline = iou.ParserCmdline
	log.Info("vols", "vols", iouPod.Spec.Volumes)

	parserPod := newParserPod(iouPod)
	curParserPod := &corev1.Pod{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: iouPod.Name+"-parser", Namespace: instance.Namespace}, curParserPod)
	if err != nil && errors.IsNotFound(err) {
		log.Info("Creating a new parser Pod", "Name", parserPod.Name)
		err = r.client.Create(context.TODO(), job)
		if err != nil {
			log.Error(err, "Error creating Job")
			return err
		}
	} else if err != nil {
		log.Error(err, "Error getting Job")
		return err
	}
	return nil
}*/


var chanMap = make(map[string]chan bool)

func (r *ReconcileBenchmarkTesting) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling BenchmarkTesting")

	// Fetch the BenchmarkTesting instance
	instance := &benchmarkingv1alpha1.BenchmarkTesting{}
	err := r.client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	reqLogger.Info("spec", "spec", instance.Spec)

	if instance.GetDeletionTimestamp() != nil {
		log.Info("Being deleted... ", "finalizers", instance.GetFinalizers())
		if contains(instance.GetFinalizers(), "ControlOpFinalizer") {
			log.Info("Removing finalizer")
			for i, _ := range instance.Spec.ControlOpUnits {
				copName := instance.Name + "-cop-" + strconv.Itoa(i)
				log.Info("Sending true to goroutine...", "name", copName)
				chanMap[copName] <- true
				log.Info("...done")
			}
			instance.SetFinalizers(remove(instance.GetFinalizers(), "ControlOpFinalizer"))
			err := r.client.Update(context.TODO(), instance)
			if err != nil {
				log.Error(err, "error")
				return reconcile.Result{}, err
			}
			log.Info("Removed finalizer")
		}
	} else {
		for i, cop := range instance.Spec.ControlOpUnits {
			copName := instance.Name + "-cop-" + strconv.Itoa(i)
			_, chanExists := chanMap[copName]
			if !chanExists && validateCop(instance, cop) {
				chanMap[copName] = make(chan bool)
				go r.doControlOps(instance, cop, copName, chanMap[copName])
				instance.SetFinalizers([]string{"ControlOpFinalizer"})
				err := r.client.Update(context.TODO(), instance)
				if err != nil {
					return reconcile.Result{}, err
				}
			}
		}
	}

	var podNames []string
	for i, iou := range instance.Spec.IoOpUnits {
		// Get config for running the IOU app (config file, cmdline string, ConfigMap name)
		appConfig, err := r.GetCommandMap(iou.ConfigName, instance.Namespace)
		if err != nil {
			return reconcile.Result{}, err
		}
		appConfig.cmdline = iou.AppCmdline

		parserConfig, err := r.GetCommandMap(iou.OutputParser, instance.Namespace)
		if err != nil {
			return reconcile.Result{}, err
		}
		parserConfig.cmdline = iou.ParserCmdline

		// Create Job object for the IOU
		job := newJobForCR(instance, iou, instance.Name + "-job-" + strconv.Itoa(i), appConfig, parserConfig)
		if err := controllerutil.SetControllerReference(instance, job, r.scheme); err != nil {
			return reconcile.Result{}, err
		}

		// Check if the Job already exists
		curJob := &batchv1.Job{}
		err = r.client.Get(context.TODO(), types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, curJob)
		if err != nil && errors.IsNotFound(err) {
			// Wasn't found, so instantiate the previously created Job object
			reqLogger.Info("Creating a new Job", "Job.Namespace", job.Namespace, "Job.Name", job.Name)
			err = r.client.Create(context.TODO(), job)
			if err != nil {
				log.Error(err, "Error creating Job")
				return reconcile.Result{}, err
			}
		} else if err != nil {
			log.Error(err, "Error getting Job")
			return reconcile.Result{}, err
		}

		// Job exists now, get its Pod
		jobPods := &corev1.PodList{}
		opts := []client.ListOption {
			client.InNamespace(job.Namespace),
			client.MatchingLabels{"job-name": job.Name},
		}
		err = r.client.List(context.TODO(), jobPods, opts...)
		if err != nil {
			if errors.IsNotFound(err) {
				reqLogger.Info("not found")
			}
			return reconcile.Result{}, err
		} else {
			// Add each Pod in the Job the CR's Pod list
			// Also, if the Pod is done ("Succeeded"), create the parser Pod to collect the output
			for _, pod := range jobPods.Items {
				podNames = append(podNames, pod.Name)
				reqLogger.Info("pod", "pod", pod.Status)
				if pod.Status.Phase == "Succeeded" {
					kubeconfig := os.Getenv("KUBECONFIG")
					//config, err := rest.InClusterConfig()
					config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
					if err != nil {
						reqLogger.Info("err", "err", err)
					}
					cs, err := kubernetes.NewForConfig(config)
					req := cs.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{},)
					readCloser, err := req.Stream()
					if err != nil {
						reqLogger.Info("error", "error", err)
					} else {
						//sendResults(readCloser)
						sendResultsES(readCloser, instance)
					}
				}
			}
		}
	}

	if !reflect.DeepEqual(podNames, instance.Status.PodNames) {
		instance.Status.PodNames = podNames
		err := r.client.Status().Update(context.TODO(), instance)
		if err != nil {
			reqLogger.Error(err, "Failed to update pod names")
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

func sendResultsES(readCloser io.ReadCloser, cr *benchmarkingv1alpha1.BenchmarkTesting) error {
	results := make(map[string]float64)
	buf := new(bytes.Buffer)

	scanner := bufio.NewScanner(readCloser)
	for scanner.Scan() {
		s := strings.Split(scanner.Text(), ",")
		val, _ := strconv.ParseFloat(s[1], 64)
		results[s[0]] = val
		log.Info("Collected result", "name", s[0], "val", val)
	}

	result := Result{Config: cr, Results: results}
	//result := Result{}
	//result.config = cr
	//result.results = results
	log.Info("result", "result", results)
	log.Info("result", "result", result)

	//err := json.NewEncoder(buf).Encode(cr)
	err := json.NewEncoder(buf).Encode(result)
	if err != nil {
		fmt.Println("error", err)
	} else {
		log.Info("obj", "obj", buf.String())
		//req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
		req, err := http.NewRequest("POST", "http://10.103.129.44:9200/testing2/_doc/", buf)
		if err != nil {
			fmt.Println("error", err)
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
        		fmt.Println("http error", err)
		}
		defer resp.Body.Close()

		fmt.Println("response Status:", resp.Status)
		fmt.Println("response Headers:", resp.Header)
		body, _ := ioutil.ReadAll(resp.Body)
		fmt.Println("response Body:", string(body))
	}

	return nil
}

/*
func sendResults(readCloser io.ReadCloser) error {
	pusher := push.New("kubes1:9093/", "benchmarktesting")
	scanner := bufio.NewScanner(readCloser)
	for scanner.Scan() {
		s := strings.Split(scanner.Text(), ",")
		val, _ := strconv.ParseFloat(s[1], 64)
		g := prometheus.NewGauge(prometheus.GaugeOpts {
			Name: s[0],
		})
		g.Set(val)
		pusher.Collector(g)
		log.Info("Collected gague", "name", s[0], "val", val)
	}
	err := pusher.Push()

	if err != nil {
		fmt.Println("error", err)
	}

	return nil
}*/

func pushGauge(name string, val float64) error {
	log.Info("Pushing gague", "name", name, "val", val)

	gauge := prometheus.NewGauge(prometheus.GaugeOpts {
		Name: name,
	})

	gauge.Set(val)
	err := push.New("kubes1:9093/", "bencharktesting").
		Collector(gauge).
		Push()
	if err != nil {
		fmt.Println("error", err)
	}

	return nil
	//return gauge
}

func validateCop(cr *benchmarkingv1alpha1.BenchmarkTesting, cop benchmarkingv1alpha1.ControlOpUnit) bool {
	log.Info("asd", "asd", cop)
	if cop.Type == "snapshot" {
		/*
		if cop.SnapshotOpArgs == {} {
			log.Error(nil, "Missing SnapshotOpArgs", cop)
			return false
		}*/
	} else if cop.Type == "provision" {
		/*if cop.ProvisionOpArgs == {} {
			log.Error(nil, "Missing ProvisionOpArgs", cop)
			return false
		}*/

		_, err := resource.ParseQuantity(cop.ProvisionOpArgs.Size)
		if err != nil {
			log.Error(err, "ProvisionOpArg.Size is invalid", "size", cop.ProvisionOpArgs.Size)
			return false
		}
	} else {
		log.Error(nil, "Invalid type given, should be either \"snapshot\" or \"provision\"", "type", cop.Type)
		return false
	}

	return true
}

// TODO: How to handle failed control op?
func (r *ReconcileBenchmarkTesting) doControlOps(cr *benchmarkingv1alpha1.BenchmarkTesting, cop benchmarkingv1alpha1.ControlOpUnit, copName string, c chan bool) {
	var count uint = 0

	log.Info("cop", "cop", cop.StartDelay)
	//if cop.StartDelay != "" {
	//	time.Sleep(time.Duration(cop.StartDelay)*time.Second)
	//}

	numSnapshotsCounter := prometheus.NewCounter(prometheus.CounterOpts {
		Name: "num_snapshots",
		Help: "Number of snapshots taken",
	})

	for {
		select {
		case <- c:
			log.Info("Exiting goroutine")
			delete(chanMap, copName); 
			return
		default:
			if count == cop.Interval {
				count = 0
				ts := time.Now().UnixNano() / 1000000
				if cop.Type == "snapshot" {
					log.Info("Creating snapshot")
					snapshot := newSnapshot(copName+"-sn-"+strconv.Itoa(int(ts)), cr.Namespace, cop.SnapshotOpArgs)
					log.Info("snapshot", "obj", snapshot)
					if err := controllerutil.SetControllerReference(cr, snapshot, r.scheme); err != nil {
						log.Error(err, "Error making snapshot child of CR")
					}
					err := r.client.Create(context.TODO(), snapshot)

					if err != nil {
						log.Error(err, "Error creating snapshot")
					}

					numSnapshotsCounter.Inc()
/*
					if err := push.New("kubes1:9093/", "bencharktesting").
						Collector(numSnapshotsCounter).
						Push(); err != nil {
						fmt.Println("error", err)
					}
*/
				} else if cop.Type == "provision" {
					log.Info("Creating provision")
					vol := newVolume(copName+"-vol-"+strconv.Itoa(int(ts)), cr.Namespace, cr.Spec.StorageClass, cop.ProvisionOpArgs)
					log.Info("volume", "obj", vol)
					if err := controllerutil.SetControllerReference(cr, vol, r.scheme); err != nil {
						log.Error(err, "Error making volume child of CR")
					}
					err := r.client.Create(context.TODO(), vol)
					if err != nil {
						log.Error(err, "Error creating vol")
					}
				}
			}
			time.Sleep(time.Second)
			count += 1
		}
	}
}

func newSnapshot(name string, namespace string, opArgs benchmarkingv1alpha1.SnapshotOpArgsSpec) *snapshotcrd.VolumeSnapshot {
	return &snapshotcrd.VolumeSnapshot {
		TypeMeta: metav1.TypeMeta {
			APIVersion: "snapshot.storage.k8s.io/v1alpha1",
			Kind: "VolumeSnapshot",
		},
		ObjectMeta: metav1.ObjectMeta {
			Name:	name,
			Namespace: namespace,
		},
		Spec: snapshotcrd.VolumeSnapshotSpec {
			VolumeSnapshotClassName: &opArgs.SnapshotClass,
			Source: &corev1.TypedLocalObjectReference {
				Name: opArgs.SnapshotTarget,
				Kind: "PersistentVolumeClaim",
			},
		},
	}
}

func newVolume(name string, namespace string, storageClass string, opArgs benchmarkingv1alpha1.ProvisionOpArgsSpec) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim {
		ObjectMeta: metav1.ObjectMeta {
			Name:	name,
			Namespace: namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec {
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &storageClass,
			Resources: corev1.ResourceRequirements {
				Requests: corev1.ResourceList {
					corev1.ResourceStorage: resource.MustParse(opArgs.Size),
				},
			},
		},
	}
}

func newJobForCR(cr *benchmarkingv1alpha1.BenchmarkTesting, iou benchmarkingv1alpha1.IoOpUnit, name string, appConfig ScriptConfig, parserConfig ScriptConfig) *batchv1.Job {
	var image string
	var parserImage string
	if iou.Image == "" {
		image = "benchmarking/fio:3.19-fix"
	} else {
		image = iou.Image
	}

	if iou.ParserImage == "" {
		parserImage = "ubuntu:18.04"
	} else {
		parserImage = iou.ParserImage
	}

	mode := int32(0744)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta {
			Name:      name,
			Namespace: cr.Namespace,
		},
		Spec: batchv1.JobSpec {
			Template: corev1.PodTemplateSpec {
				Spec: corev1.PodSpec {
					RestartPolicy:	"Never",
					Affinity: &corev1.Affinity {
						NodeAffinity: &corev1.NodeAffinity {
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector {
								NodeSelectorTerms: []corev1.NodeSelectorTerm {
									{
										MatchExpressions: []corev1.NodeSelectorRequirement {
											{
												Key: "benchmarking",
												Operator: "In",
												Values: []string{"benchmark-runner"},
											},
										},
									},
								},
							},
						},
					},
					InitContainers: []corev1.Container {
						{
							Name:    "test-container",
							Image:   image,
							Command: []string{"sh", "-c"},
							Args: []string{appConfig.cmdline},
							VolumeMounts: []corev1.VolumeMount {
								{
									Name: "config",
									MountPath: "/var/config/",
								},
								{
									Name: "data",
									MountPath: iou.WorkingDir,
								},
								{
									Name: "output",
									MountPath: iou.OutputDir,
								},
							},
							Env: []corev1.EnvVar {
								{
									Name: "CONFIG_FILE",
									Value: "/var/config/"+appConfig.scriptName,
								},
							},
						},
					},
					Containers: []corev1.Container {
						{
							Name:    "parser-container",
							Image:   parserImage,
							Command: []string{"sh", "-c"},
							Args: []string{parserConfig.cmdline},
							WorkingDir: iou.OutputDir,
							VolumeMounts: []corev1.VolumeMount {
								{
									Name: "parser",
									MountPath: iou.OutputDir+"/"+parserConfig.scriptName,
									SubPath: parserConfig.scriptName,
								},
								{
									Name: "output",
									MountPath: iou.OutputDir,
								},
							},
							Env: []corev1.EnvVar {
								{
									Name: "OUTPUT_DIR",
									Value: iou.OutputDir,
								},
							},
						},
					},
					Volumes: []corev1.Volume {
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource {
								ConfigMap: &corev1.ConfigMapVolumeSource {
									LocalObjectReference: corev1.LocalObjectReference {
										Name: appConfig.cmName,
									},
									Items: []corev1.KeyToPath {
										{
											Key: appConfig.scriptName,
											Path: appConfig.scriptName,
										},
									},
								},
							},
						},
						{
							Name: "parser",
							VolumeSource: corev1.VolumeSource {
								ConfigMap: &corev1.ConfigMapVolumeSource {
									LocalObjectReference: corev1.LocalObjectReference {
										Name: parserConfig.cmName,
									},
									Items: []corev1.KeyToPath {
										{
											Key: parserConfig.scriptName,
											Path: parserConfig.scriptName,
											Mode: &mode,
										},
									},
								},
							},
						},
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource {
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource {
									ClaimName: iou.PVCName,
								},
							},
						},
						{
							Name: "output",
							VolumeSource: corev1.VolumeSource {
								EmptyDir: &corev1.EmptyDirVolumeSource {
								},
							},
						},
					},
				},
			},
		},
	}
}
