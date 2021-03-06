package health

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/kubectl/pkg/polymorphichelpers"
	"k8s.io/kubectl/pkg/util/podutils"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kudoapi "github.com/kudobuilder/kudo/pkg/apis/kudo/v1beta1"
	"github.com/kudobuilder/kudo/pkg/engine"
	"github.com/kudobuilder/kudo/pkg/engine/resource"
	"github.com/kudobuilder/kudo/pkg/kudoctl/clog"
)

func isJobTerminallyFailed(job *batchv1.Job) (bool, string) {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			log.Printf("HealthUtil: Job %q has failed: %s", job.Name, c.Message)
			return true, c.Message
		}
	}
	return false, ""
}

func IsDeleted(client client.Client, discovery discovery.CachedDiscoveryInterface, objs []runtime.Object) error {
	for _, obj := range objs {
		key, err := resource.ObjectKeyFromObject(obj, discovery)
		if err != nil {
			return err
		}
		newObj := obj.DeepCopyObject()
		err = client.Get(context.TODO(), key, newObj)
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("%s/%s is not deleted", key.Namespace, key.Name)
		}
	}
	return nil
}

// IsHealthy returns whether an object is healthy. Must be implemented for each type.
func IsHealthy(obj runtime.Object) error {
	if obj == nil {
		return nil
	}
	unstructMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return err
	}

	objUnstructured := &unstructured.Unstructured{Object: unstructMap}
	switch obj := obj.(type) {
	case *apiextv1beta1.CustomResourceDefinition:
		for _, c := range obj.Status.Conditions {
			if c.Type == apiextv1beta1.Established && c.Status == apiextv1beta1.ConditionTrue {
				log.Printf("CRD %s is now healthy", obj.Name)
				return nil
			}
		}
		msg := fmt.Sprintf("CRD %s is not healthy ( Conditions: %v )", obj.Name, obj.Status.Conditions)
		return errors.New(msg)
	case *apiextv1.CustomResourceDefinition:
		for _, c := range obj.Status.Conditions {
			if c.Type == apiextv1.Established && c.Status == apiextv1.ConditionTrue {
				log.Printf("CRD %s is now healthy", obj.Name)
				return nil
			}
		}
		msg := fmt.Sprintf("CRD %s is not healthy ( Conditions: %v )", obj.Name, obj.Status.Conditions)
		return errors.New(msg)
	case *appsv1.StatefulSet:
		statusViewer := &polymorphichelpers.StatefulSetStatusViewer{}
		msg, done, err := statusViewer.Status(objUnstructured, 0)
		if err != nil {
			return err
		}
		if !done {
			log.Printf("HealthUtil: Statefulset %v is NOT healthy. %s", obj.Name, msg)
			return errors.New(msg)
		}
		log.Printf("Statefulset %v is marked healthy\n", obj.Name)
		return nil
	case *appsv1.Deployment:
		statusViewer := &polymorphichelpers.DeploymentStatusViewer{}
		msg, done, err := statusViewer.Status(objUnstructured, 0)
		if err != nil {
			return err
		}
		if !done {
			log.Printf("HealthUtil: Deployment %v is NOT healthy. %s", obj.Name, msg)
			return errors.New(msg)
		}
		clog.V(2).Printf("Deployment %v is marked healthy\n", obj.Name)
		return nil
	case *batchv1.Job:

		if obj.Status.Succeeded == int32(1) {
			// Done!
			log.Printf("HealthUtil: Job %q is marked healthy", obj.Name)
			return nil
		}
		if terminal, msg := isJobTerminallyFailed(obj); terminal {
			return fmt.Errorf("%wHealthUtil: Job %q has failed terminally: %s", engine.ErrFatalExecution, obj.Name, msg)
		}

		return fmt.Errorf("job %q still running or failed", obj.Name)
	case *kudoapi.Instance:
		// if there is no scheduled plan, then we're done
		if obj.Spec.PlanExecution.PlanName == "" {
			return nil
		}

		return fmt.Errorf("instance %s/%s active plan is in state %v", obj.Namespace, obj.Name, obj.Spec.PlanExecution.Status)

	case *corev1.Pod:
		if obj.Status.Phase == corev1.PodRunning && podutils.IsPodReady(obj) {
			return nil
		}
		return fmt.Errorf("pod %q is not running yet: %s", obj.Name, obj.Status.Phase)

	case *corev1.Namespace:
		if obj.Status.Phase == corev1.NamespaceActive {
			return nil
		}
		return fmt.Errorf("namespace %s is not active: %s", obj.Name, obj.Status.Phase)

	// unless we build logic for what a healthy object is, assume it's healthy when created.
	default:
		log.Printf("HealthUtil: Unknown type %s is marked healthy by default", reflect.TypeOf(obj))
		return nil
	}
}
