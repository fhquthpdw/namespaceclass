/*
Copyright 2025.

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

package controller

import (
    "context"
    "fmt"

    "k8s.io/api/core/v1"
    networkingv1 "k8s.io/api/networking/v1"
    "k8s.io/apimachinery/pkg/api/errors"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/labels"
    "k8s.io/apimachinery/pkg/runtime"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/builder"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/event"
    "sigs.k8s.io/controller-runtime/pkg/handler"
    "sigs.k8s.io/controller-runtime/pkg/log"
    "sigs.k8s.io/controller-runtime/pkg/predicate"
    "sigs.k8s.io/controller-runtime/pkg/reconcile"

    corev1 "akuity.io/homework/api/v1"
)

const (
    NamespaceLabelName = "namespaceclass.akuity.io/name"
)

// NamespaceClassReconciler reconciles a NamespaceClass object
type NamespaceClassReconciler struct {
    client.Client
    Scheme *runtime.Scheme
}

type NSClassMap map[string]corev1.NamespaceClass

// +kubebuilder:rbac:groups=core.akuity.io,resources=namespaceclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.akuity.io,resources=namespaceclasses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.akuity.io,resources=namespaceclasses/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the NamespaceClass object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.2/pkg/reconcile
func (r *NamespaceClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
    _ = log.FromContext(ctx)

    // get all cluster Namespaces
    var nsList v1.NamespaceList
    if err = r.List(ctx, &nsList, &client.ListOptions{
        LabelSelector: labels.Everything(),
    }); err != nil {
        if errors.IsNotFound(err) {
            return res, nil
        }
        return res, err
    }

    nsClassMap, err := r.getNSClassMap(ctx)
    if err != nil {
        log.Log.Error(err, "get namespace class map error")
    }
    for _, ns := range nsList.Items {
        r.tackleOneNamespace(ctx, ns, nsClassMap)
    }

    return res, nil
}

func (r *NamespaceClassReconciler) getNSClassMap(ctx context.Context) (NSClassMap, error) {
    var err error
    nsClassMap := make(NSClassMap)
    var nsClassList corev1.NamespaceClassList
    if err = r.List(ctx, &nsClassList, &client.ListOptions{
        LabelSelector: labels.Everything(),
    }); err != nil {
        if errors.IsNotFound(err) {
            return nsClassMap, nil
        }
        return nsClassMap, err
    }
    for _, nsClass := range nsClassList.Items {
        nsClassMap[nsClass.Name] = nsClass
    }

    return nsClassMap, nil
}

func (r *NamespaceClassReconciler) tackleOneNamespace(ctx context.Context, ns v1.Namespace, nsClassMap NSClassMap) {
    var err error
    labelName := ns.Labels[NamespaceLabelName]
    nsClass, ok := nsClassMap[labelName]
    if !ok { // clean all useless network policy in this namespace
        if err = r.delNetworkPolicy(ctx, ns); err != nil {
            log.Log.Error(err, "")
        }
        return
    }

    if err = r.updateNetworkPolicy(ctx, ns, nsClass); err != nil {
        log.Log.Error(err, "")
    }
    log.Log.Info(fmt.Sprintf("create/update network policy in namespace '%s' success", ns.Name))
}

func (r *NamespaceClassReconciler) genNetworkPolicyIns(ns v1.Namespace) networkingv1.NetworkPolicy {
    return networkingv1.NetworkPolicy{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("%s-networkpolicy", ns.Name),
            Namespace: ns.Name,
        },
    }
}

func (r *NamespaceClassReconciler) delNetworkPolicy(ctx context.Context, ns v1.Namespace) (err error) {
    networkPolicy := r.genNetworkPolicyIns(ns)

    if err = r.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: networkPolicy.Name}, &networkingv1.NetworkPolicy{}); err != nil {
        if !errors.IsNotFound(err) {
            return fmt.Errorf("get network policy '%s' in namespace '%s' error: %s", networkPolicy.Name, ns.Name, err)
        }
        return nil
    }

    if err = r.Delete(ctx, &networkPolicy); err != nil {
        err = fmt.Errorf("delete network policy '%s' in namespace '%s' error: %s", networkPolicy.Name, ns.Name, err)
    }
    log.Log.Info(fmt.Sprintf("delete network policy '%s' in namespace '%s' success", networkPolicy.Name, ns.Name))
    return
}

func (r *NamespaceClassReconciler) updateNetworkPolicy(ctx context.Context, ns v1.Namespace, nsClass corev1.NamespaceClass) (err error) {
    networkPolicy := r.genNetworkPolicyIns(ns)

    // delete network policy
    if !nsClass.DeletionTimestamp.IsZero() { // Delete
        return r.delNetworkPolicy(ctx, ns)
    }

    // create or update network policy
    networkPolicy.Spec = nsClass.Spec.NetworkPolicy
    if err = r.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: networkPolicy.Name}, &networkingv1.NetworkPolicy{}); err != nil {
        if !errors.IsNotFound(err) {
            return fmt.Errorf("get network policy '%s' in namespace '%s' error: %s", networkPolicy.Name, ns.Name, err)
        }
        if err = r.Create(ctx, &networkPolicy); err != nil {
            return fmt.Errorf("create network policy '%s' in namespace '%s' error: %s", networkPolicy.Name, ns.Name, err)
        }
    } else {
        if err = r.Update(ctx, &networkPolicy); err != nil {
            return fmt.Errorf("update network policy '%s' in namespace '%s' error: %s", networkPolicy.Name, ns.Name, err)
        }
    }
    return nil
}

func genNamespacePredicates() predicate.Funcs {
    return predicate.Funcs{
        CreateFunc: func(e event.CreateEvent) bool {
            log.FromContext(context.TODO()).Info("Namespace created", "name", e.Object.GetName())
            return true
        },
        UpdateFunc: func(e event.UpdateEvent) bool {
            log.FromContext(context.TODO()).Info("Namespace updated", "name", e.ObjectNew.GetName())
            return true
        },
        DeleteFunc: func(e event.DeleteEvent) bool {
            log.FromContext(context.TODO()).Info("Namespace deleted", "name", e.Object.GetName())
            return true
        },
    }
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        Watches(
            &v1.Namespace{},
            handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, a client.Object) []reconcile.Request {
                // get all NamespacesClass
                nsClassMap, err := r.getNSClassMap(ctx)
                if err != nil {
                    log.Log.Error(err, "get namespace class map error")
                    return []reconcile.Request{}
                }

                // get modified Namespace
                ns, ok := a.(*v1.Namespace)
                if !ok {
                    log.Log.Error(nil, "expected a namespace object", "object", a)
                    return []reconcile.Request{}
                }

                //
                r.tackleOneNamespace(ctx, *ns, nsClassMap)
                return []reconcile.Request{}
            }),
            builder.WithPredicates(genNamespacePredicates())).
        For(&corev1.NamespaceClass{}).
        Named("namespaceclass").
        Complete(r)
}
