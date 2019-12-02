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

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"oudishen.net/podset/api/v1alpha1"
)

const (
	PodSetFinalizer = "podset.sample.oudishen.net"
)

func (r *PodSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PodSet{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}

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
	podSet := &v1alpha1.PodSet{}
	err := r.Get(context.TODO(), req.NamespacedName, podSet)
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
			"podset": req.Name,
		},
	)

	scope := &PosSetScope{
		Client: r.Client,
		ctx:    context.TODO(),
		logger: logger.WithName("PodSetScope"),
		scheme: r.Scheme,
		podSet: podSet,
		pods:   pods,
	}

	defer func() {
		if err := scope.Close(); err != nil {
			logger.Info("scope.Close error: " + err.Error())
		}
	}()

	// Handle deleted clusters
	if !podSet.DeletionTimestamp.IsZero() {
		return scope.ReconcileDelete()
	}

	// Handle non-deleted clusters
	return scope.ReconcileNormal()
}

type PosSetScope struct {
	client.Client
	ctx    context.Context
	logger logr.Logger
	scheme *runtime.Scheme
	podSet *v1alpha1.PodSet
	pods   *corev1.PodList
}

func (s *PosSetScope) Close() error {
	if err := s.Client.Update(context.TODO(), s.podSet); err != nil {
		return errors.Wrap(err, "s.client.Update")
	}
	if len(s.podSet.Finalizers) == 0 {
		return nil
	}
	if err := s.Client.Status().Update(context.TODO(), s.podSet); err != nil {
		return errors.Wrap(err, "s.Client.Status().Update")
	}
	return nil
}

func (s *PosSetScope) ReconcileDelete() (reconcile.Result, error) {
	s.logger.Info("ReconcileDelete")

	if len(s.pods.Items) == 0 {
		controllerutil.RemoveFinalizer(s.podSet, PodSetFinalizer)
		return reconcile.Result{}, nil
	}

	for _, pod := range s.pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		s.logger.Info("Deleting a Pod", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
		if err := s.Delete(context.TODO(), pod.DeepCopy()); err != nil {
			if !apierr.IsNotFound(err) {
				return reconcile.Result{}, errors.Wrap(err, "r.Delete pod")
			}
		}
	}

	return reconcile.Result{}, nil
}

func (s *PosSetScope) ReconcileNormal() (reconcile.Result, error) {
	controllerutil.AddFinalizer(s.podSet, PodSetFinalizer)

	replicas := int32(len(s.pods.Items))
	s.podSet.Status.AvailableReplicas = replicas
	s.logger.Info("", "Update Status.AvailableReplicas to ", replicas)
	if err := s.Status().Update(context.TODO(), s.podSet); err != nil {
		s.logger.Info("", "Failed to update Status.AvailableReplicas to ", replicas)
		return reconcile.Result{}, errors.Wrap(err, "s.Status().Update")
	}

	n := int(s.podSet.Spec.Replicas - replicas)
	if n > 0 {
		for i := 0; i < n; i++ {
			// Define a new Pod object
			pod := newPodForCR(s.podSet)

			// Set PodSet instance as the owner and controller
			if err := controllerutil.SetControllerReference(s.podSet, pod, s.scheme); err != nil {
				return reconcile.Result{}, errors.Wrap(err, "controllerutil.SetControllerReference")
			}

			s.logger.Info("Creating a Pod", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
			if err := s.Create(context.TODO(), pod); err != nil {
				return reconcile.Result{}, errors.Wrap(err, "s.Create pod")
			}
		}

		// Pod created successfully - don't requeue
		return reconcile.Result{}, nil
	}

	if n < 0 {
		for i := 0; i < -n; i++ {
			pod := s.pods.Items[i].DeepCopy()
			if pod.DeletionTimestamp != nil {
				continue
			}

			s.logger.Info("Deleting a Pod", "Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
			if err := s.Delete(context.TODO(), pod); err != nil {
				if !apierr.IsNotFound(err) {
					return reconcile.Result{}, errors.Wrap(err, "s.Delete pod")
				}
			}
		}

		// Pod delete successfully - don't requeue
		return reconcile.Result{}, nil
	}

	// Pod already exists - don't requeue
	s.logger.Info("Skip reconcile: replicas already adjust")
	return reconcile.Result{}, nil
}

// newPodForCR returns a busybox pod with the same name/namespace as the cr
func newPodForCR(cr *v1alpha1.PodSet) *corev1.Pod {
	labels := map[string]string{
		"podset": cr.Name,
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
