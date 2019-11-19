/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"oudishen.net/podset/api/v1alpha1"
)

// PodSetReconciler reconciles a PodSet object
type PodSetReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=sample.oudishen.net,resources=podsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sample.oudishen.net,resources=podsets/status,verbs=get;update;patch

func (r *PodSetReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("podset", req.NamespacedName)
	logger.Info("Reconciling")

	// Fetch the PodSet instance
	instance := &v1alpha1.PodSet{}
	err := r.Get(context.TODO(), req.NamespacedName, instance)
	if err != nil {
		if apierr.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, errors.Wrap(err, "r.Get")
	}

	pods := &corev1.PodList{}
	err = r.List(
		context.TODO(),
		pods,
		client.InNamespace(req.Namespace),
		&client.MatchingLabels{
			"app": req.Name,
		},
	)
	replicas := int32(len(pods.Items))

	logger.Info("", "Update Status.AvailableReplicas to ", replicas)
	instance.Status.AvailableReplicas = replicas
	if err := r.Update(context.TODO(), instance); err != nil {
		logger.Info("", "Failed to update Status.AvailableReplicas to ", replicas)
		return reconcile.Result{}, errors.Wrap(err, "r.Status().Update")
	}

	n := int(instance.Spec.Replicas - replicas)
	if n > 0 {
		for i := 0; i < n; i++ {
			// Define a new Pod object
			pod := newPodForCR(instance)

			// Set PodSet instance as the owner and controller
			if err := controllerutil.SetControllerReference(instance, pod, r.Scheme); err != nil {
				return reconcile.Result{}, errors.Wrap(err, "controllerutil.SetControllerReference")
			}

			logger.Info("Creating a Pod", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
			err = r.Create(context.TODO(), pod)
			if err != nil {
				return reconcile.Result{}, errors.Wrap(err, "r.Create pod")
			}
		}

		// Pod created successfully - don't requeue
		return reconcile.Result{}, nil
	}

	if n < 0 {
		for i := 0; i < -n; i++ {
			pod := pods.Items[i].DeepCopy()
			logger.Info("Deleting a Pod", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
			err = r.Delete(context.TODO(), pod)
			if err != nil {
				return reconcile.Result{}, errors.Wrap(err, "r.Delete pod")
			}
		}

		// Pod delete successfully - don't requeue
		return reconcile.Result{}, nil
	}

	// Pod already exists - don't requeue
	logger.Info("Skip reconcile: replicas already adjust")
	return reconcile.Result{}, nil
}

// newPodForCR returns a busybox pod with the same name/namespace as the cr
func newPodForCR(cr *v1alpha1.PodSet) *corev1.Pod {
	labels := map[string]string{
		"app": cr.Name,
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name + "-pod-" + rand.String(5),
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "busybox",
					Image:   "busybox:1",
					Command: []string{"sleep", "3600"},
				},
			},
		},
	}
}

func (r *PodSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PodSet{}).
		Watches(
			&source.Kind{Type: &corev1.Pod{}},
			&handler.EnqueueRequestForOwner{
				IsController: true,
				OwnerType:    &v1alpha1.PodSet{},
			},
		).
		Complete(r)
}
