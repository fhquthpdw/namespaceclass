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
	"bytes"
	"context"
	"fmt"
	"os"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
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
func (r *NamespaceClassReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (res ctrl.Result, err error) {
	_ = log.FromContext(ctx)

	nsList, err := r.getAllNS(ctx)
	if err != nil {
		log.Log.Error(err, "get all namespaces error")
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

func (r *NamespaceClassReconciler) genNamespacedResources(namespaceClassName string) ([]*unstructured.Unstructured, error) {
	configFilePath := fmt.Sprintf("config/namespace-class/%s.yaml", namespaceClassName)
	data, err := os.ReadFile(configFilePath)
	if err != nil {
		return nil, fmt.Errorf("read yaml config file: %s", err)
	}

	// decode yaml config file
	var resources []*unstructured.Unstructured
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 1024)
	for {
		var rawObj runtime.RawExtension
		if err = decoder.Decode(&rawObj); err != nil {
			break
		}

		// decode to unstructured
		obj, _, err := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme).Decode(rawObj.Raw, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("decode to unstructured error: %w", err)
		}

		unstructuredMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			return nil, fmt.Errorf("convert to unstructured error: %w", err)
		}

		unstructuredObj := &unstructured.Unstructured{Object: unstructuredMap}
		resources = append(resources, unstructuredObj)
	}

	return resources, nil
}

func (r *NamespaceClassReconciler) getAllNS(ctx context.Context) (nsList v1.NamespaceList, err error) {
	if err = r.List(ctx, &nsList, &client.ListOptions{
		LabelSelector: labels.Everything(),
	}); err != nil {
		if errors.IsNotFound(err) {
			return nsList, nil
		}
		return nsList, err
	}

	return
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
	nsClass, ok := nsClassMap[ns.Labels[NamespaceLabelName]]
	if !ok {
		return
	}

	// update or create namespaced resources
	if err := r.updateNamespacedResource(ctx, ns, nsClass); err != nil {
		log.Log.Error(err, "")
		return
	}
}

func (r *NamespaceClassReconciler) delNamespacedResource(ctx context.Context, ns v1.Namespace, nsClass corev1.NamespaceClass) (err error) {
	resources, err := r.genNamespacedResources(nsClass.Name)
	if err != nil {
		return fmt.Errorf("gen namespaced resources error: %s", err)
	}

	for _, resource := range resources {
		key := client.ObjectKey{Namespace: ns.Name, Name: resource.GetName()}
		if err = r.Get(ctx, key, resource); err != nil {
			if !errors.IsNotFound(err) {
				return fmt.Errorf("get %s in namespace '%s' error: %s", resource.GetName(), ns.Name, err)
			}
			return nil
		}

		if err = r.Delete(ctx, resource); err != nil {
			err = fmt.Errorf("delete %s in namespace '%s' error: %s", resource.GetName(), ns.Name, err)
		} else {
			log.Log.Info(fmt.Sprintf("delete %s in namespace '%s' success", resource.GetName(), ns.Name))
		}
	}

	return nil
}

func (r *NamespaceClassReconciler) updateNamespacedResource(ctx context.Context, ns v1.Namespace, nsClass corev1.NamespaceClass) (err error) {
	resources, err := r.genNamespacedResources(nsClass.Name)
	if err != nil {
		return fmt.Errorf("gen namespaced resources error: %s", err)
	}

	// create or update namespaced resources
	for _, resource := range resources {
		key := client.ObjectKey{Namespace: ns.Name, Name: resource.GetName()}
		fmt.Println("**** update ****")
		fmt.Printf("%+v\n", key)
		fmt.Println("**** update ****")

		if err = r.Get(ctx, key, resource); err != nil {
			if !errors.IsNotFound(err) {
				return fmt.Errorf("get %s in namespace '%s' error: %s", resource.GetName(), ns.Name, err)
			}
			if err = r.Create(ctx, resource); err != nil {
				return fmt.Errorf("create %s in namespace '%s' error: %s", resource.GetName(), ns.Name, err)
			} else {
				log.Log.Info(fmt.Sprintf("create %s in namespace '%s' success", resource.GetName(), ns.Name))
			}
		} else {
			if err = r.Update(ctx, resource); err != nil {
				return fmt.Errorf("update %s in namespace '%s' error: %s", resource.GetName(), ns.Name, err)
			} else {
				log.Log.Info(fmt.Sprintf("update %s in namespace '%s' success", resource.GetName(), ns.Name))
			}
		}
	}

	return nil
}

func (r *NamespaceClassReconciler) genNamespacePredicates() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			ctx := context.Background()
			log.FromContext(ctx).Info("Namespace created", "name", e.Object.GetName())
			obj, ok := e.Object.(*v1.Namespace)
			if !ok {
				log.Log.Error(nil, "expected a namespace object", "object", e.Object)
				return false
			}
			nsClassMap, err := r.getNSClassMap(ctx)
			if err != nil {
				log.Log.Error(err, "get namespace class map error")
				return false
			}
			if _, ok = nsClassMap[obj.Labels[NamespaceLabelName]]; !ok {
				log.Log.Info("no namespace label matched")
				return false
			}

			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			log.FromContext(context.TODO()).Info("Namespace updated", "name", e.ObjectNew.GetName())
			oldObj, ok := e.ObjectOld.(*v1.Namespace)
			if !ok {
				log.Log.Error(nil, "expected a namespace object", "object", e.ObjectOld)
				return false
			}
			newObj, ok := e.ObjectNew.(*v1.Namespace)
			if !ok {
				log.Log.Error(nil, "expected a namespace object", "object", e.ObjectNew)
				return false
			}
			if oldObj.Labels[NamespaceLabelName] == newObj.Labels[NamespaceLabelName] {
				log.Log.Info("no label changes")
				return false
			}
			ctx := context.Background()
			nsClassMap, err := r.getNSClassMap(ctx)
			if err != nil {
				log.Log.Error(err, "get namespace class map error")
				return false
			}
			if nsClass, ok := nsClassMap[oldObj.Labels[NamespaceLabelName]]; ok {
				if err = r.delNamespacedResource(ctx, *oldObj, nsClass); err != nil {
					log.Log.Error(err, "delete namespaced resource error")
				}
				return false
			}

			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log.FromContext(context.TODO()).Info("Namespace deleted", "name", e.Object.GetName())
			return false // do nothing
		},
	}
}

func (r *NamespaceClassReconciler) genNamespaceClassPredicates() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			log.FromContext(context.TODO()).Info("Namespace Class created", "name", e.Object.GetName())
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			log.FromContext(context.TODO()).Info("Namespace Class updated", "name", e.ObjectNew.GetName())
			// TODO: I think should check if changed the name
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			log.FromContext(context.TODO()).Info("Namespace Class deleted", "name", e.Object.GetName())
			nsClass, ok := e.Object.(*corev1.NamespaceClass)
			if !ok {
				log.Log.Error(nil, "expected a namespace class object", "object", e.Object)
				return false
			}

			ctx := context.Background()
			nsList, err := r.getAllNS(ctx)
			if err != nil {
				log.Log.Error(err, "get all namespaces error")
				return false
			}
			for _, ns := range nsList.Items {
				if ns.Labels[NamespaceLabelName] != nsClass.Name {
					continue
				}
				if err = r.delNamespacedResource(ctx, ns, *nsClass); err != nil {
					log.Log.Error(err, "delete namespaced resource error")
				}
			}

			return false
		},
	}
}

func (r *NamespaceClassReconciler) watchNamespaceFun() handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		// get all NamespacesClass
		nsClassMap, err := r.getNSClassMap(ctx)
		if err != nil {
			log.Log.Error(err, "get namespace class map error")
			return []reconcile.Request{}
		}

		// get modified Namespace
		ns, ok := obj.(*v1.Namespace)
		if !ok {
			log.Log.Error(nil, "expected a namespace object", "object", obj)
			return []reconcile.Request{}
		}

		r.tackleOneNamespace(ctx, *ns, nsClassMap)
		return []reconcile.Request{}
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Watches(
			&v1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.watchNamespaceFun()),
			builder.WithPredicates(r.genNamespacePredicates()),
		).
		For(
			&corev1.NamespaceClass{},
			builder.WithPredicates(r.genNamespaceClassPredicates()),
		).
		Named("namespaceclass").
		Complete(r)
}
