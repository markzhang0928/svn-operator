/*
Copyright 2024 markzhang.

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
	"reflect"
	"sort"
	"time"

	"github.com/go-logr/logr"
	svnv1alpha1 "github.com/markzhang0928/svn-operator/api/v1alpha1"
	"github.com/markzhang0928/svn-operator/pkg/svnconfig"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	VolumeNameRepos  = "repos"
	VolumePathRepos  = "/svn"
	VolumeNameConfig = "config"
	VolumePathConfig = "/etc/svn-config/"

	ContainerNameSVN = "svn"

	LabelAppKey          = "app"
	LabelAppValue        = "subversion"
	LabelInstanceNameKey = "svn.zhangyi.chat/name"

	ConfigMapKeyAuthUserFile       = "AuthUserFile"
	ConfigMapKeyAuthzSVNAccessFile = "AuthzSVNAccessFile"
	ConfigMapKeyRepos              = "Repos"

	IndexKeySVNServer = ".spec.svnServer"

	ConditionHistoryLimit = 10
)

// SVNServerReconciler reconciles a SVNServer object
type SVNServerReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme

	// DefaultSVNServerImage is a Docker image name to run SVN server.
	DefaultSVNServerImage string
}

type GeneratorFactory struct {
	server *svnv1alpha1.SVNServer
	repos  *svnv1alpha1.SVNRepositoryList
	groups *svnv1alpha1.SVNGroupList
	users  *svnv1alpha1.SVNUserList
}

// +kubebuilder:rbac:groups=svn.zhangyi.chat,resources=svnservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=svn.zhangyi.chat,resources=svnservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=svn.zhangyi.chat,resources=svnservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=svn.zhangyi.chat,resources=svnrepositories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=svn.zhangyi.chat,resources=svnrepositories/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=svn.zhangyi.chat,resources=svnrepositories/finalizers,verbs=update
// +kubebuilder:rbac:groups=svn.zhangyi.chat,resources=svngroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=svn.zhangyi.chat,resources=svngroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=svn.zhangyi.chat,resources=svngroups/finalizers,verbs=update
// +kubebuilder:rbac:groups=svn.zhangyi.chat,resources=svnusers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=svn.zhangyi.chat,resources=svnusers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=svn.zhangyi.chat,resources=svnusers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the SVNServer object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.0/pkg/reconcile
func (r *SVNServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("svnserver", req.NamespacedName)

	svnServer := &svnv1alpha1.SVNServer{}
	err := r.Get(ctx, req.NamespacedName, svnServer)
	if err != nil {
		if errors.IsNotFound(err) {
			// The object cloud have been deleted asynchronously.
			log.Info("SVNServer not found; ignoring.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get SVNServer")
		return ctrl.Result{}, err
	}
	svc := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: svnServer.Name, Namespace: svnServer.Namespace}, svc)
	if err != nil {
		if errors.IsNotFound(err) {
			if err = r.createService(ctx, log, svnServer); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
	}

	ss := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: svnServer.Name, Namespace: svnServer.Namespace}, ss)
	if err != nil {
		if errors.IsNotFound(err) {
			if err = r.createStatefulSet(ctx, log, svnServer); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "Failed to get StatefulSet")
		return ctrl.Result{}, err
	}

	repos := &svnv1alpha1.SVNRepositoryList{}
	err = r.List(ctx, repos, client.InNamespace(svnServer.Namespace), client.MatchingFields{IndexKeySVNServer: svnServer.Name})
	if err != nil {
		log.Error(err, "Failed to list SVNRepository")
		return ctrl.Result{}, err
	}

	groups := &svnv1alpha1.SVNGroupList{}
	err = r.List(ctx, groups, client.InNamespace(svnServer.Namespace), client.MatchingFields{IndexKeySVNServer: svnServer.Name})
	if err != nil {
		log.Error(err, "Failed to list SVNGroup")
		return ctrl.Result{}, err
	}

	users := &svnv1alpha1.SVNUserList{}
	err = r.List(ctx, users, client.InNamespace(svnServer.Namespace), client.MatchingFields{IndexKeySVNServer: svnServer.Name})
	if err != nil {
		log.Error(err, "Failed to list SVNUser")
		return ctrl.Result{}, err
	}

	log.Info("reconciling SVNServer")

	factory := &GeneratorFactory{
		server: svnServer,
		repos:  repos,
		groups: groups,
		users:  users,
	}
	cm := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: svnServer.Name, Namespace: svnServer.Namespace}, cm)
	if err != nil {
		if errors.IsNotFound(err) {
			if err = r.createConfigMap(ctx, log, factory); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "Failed to get ConfigMap")
		return ctrl.Result{}, err
	}

	changed := false

	desiredSS := ss.DeepCopy()
	r.overrideWithPodTemplate(svnServer, desiredSS)
	if !reflect.DeepEqual(desiredSS, ss) {
		changed = true
		if err := r.Update(ctx, desiredSS); err != nil {
			log.Error(err, "Failed to update StatefulSet")
			return ctrl.Result{}, err
		}
	}

	desiredCM, err := r.configMapFor(factory)
	if err != nil {
		log.Error(err, "Failed to compute desired configmap")
		return ctrl.Result{}, err
	}
	if !reflect.DeepEqual(desiredCM.Data, cm.Data) {
		changed = true
		if err := r.Update(ctx, desiredCM); err != nil {
			log.Error(err, "Failed to update ConfigMap")
			return ctrl.Result{}, err
		}
	}

	if !changed {
		return ctrl.Result{}, nil
	}

	svnServer.Status.Conditions = addCondition(svnServer.Status.Conditions, svnv1alpha1.Condition{
		Type:           svnv1alpha1.ConditionTypeSynced,
		Reason:         "successfully synced",
		TransitionTime: time.Now().Format(time.RFC3339),
	})

	if err := r.Status().Update(ctx, svnServer); err != nil {
		log.Error(err, "Failed to update SVNServer status")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// Creates a StatefulSet and is corresponding Service
func (r *SVNServerReconciler) createStatefulSet(ctx context.Context, log logr.Logger, svn *svnv1alpha1.SVNServer) error {
	ss, err := r.statefulSetFor(svn)
	if err != nil {
		log.Error(err, "Failed to compute desired StatefulSet")
		return err
	}
	log = log.WithValues("StatefulSet.Namespace", ss.Namespace, "StatefulSet.Name", ss.Name)
	log.Info("Creating a new StatefulSet")
	if err := r.Create(ctx, ss); err != nil {
		log.Error(err, "Failed to create new StatefulSet")
		return err
	}
	return nil
}

func (r *SVNServerReconciler) createService(ctx context.Context, log logr.Logger, svn *svnv1alpha1.SVNServer) error {
	svc, err := r.serviceFor(svn)
	if err != nil {
		log.Error(err, "Failed to compute desired Service")
		return err
	}
	log = log.WithValues("Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
	log.Info("Creating a new Service")
	if err := r.Create(ctx, svc); err != nil {
		log.Error(err, "Failed to create new Service")
		return err
	}
	return nil
}

func (r *SVNServerReconciler) createConfigMap(ctx context.Context, log logr.Logger, f *GeneratorFactory) error {
	cm, err := r.configMapFor(f)
	if err != nil {
		log.Error(err, "Failed to compute desired ConfigMap")
		return err
	}
	log = log.WithValues("ConfigMap.Namespace", cm.Namespace, "ConfigMap.Name", cm.Name)
	log.Info("Creating a new ConfigMap")
	if err := r.Create(ctx, cm); err != nil {
		log.Error(err, "Failed to create new ConfigMap")
		return err
	}
	return nil
}

func (r *SVNServerReconciler) statefulSetFor(s *svnv1alpha1.SVNServer) (*appsv1.StatefulSet, error) {
	labels := r.labelsFor(s)
	replicas := int32(1)
	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.Name,
			Namespace: s.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
			},
			ServiceName: s.Name,
		},
	}
	r.overrideWithPodTemplate(s, ss)
	err := ctrl.SetControllerReference(s, ss, r.Scheme)
	if err != nil {
		return nil, err
	}
	return ss, nil
}

func (r *SVNServerReconciler) overrideWithPodTemplate(s *svnv1alpha1.SVNServer, ss *appsv1.StatefulSet) {
	var volumeClaimIndex int = -1
	for i := range ss.Spec.VolumeClaimTemplates {
		pvc := &ss.Spec.VolumeClaimTemplates[i]
		if pvc.Name == VolumeNameRepos {
			volumeClaimIndex = i
			break
		}
	}
	if volumeClaimIndex < 0 {
		pvc := corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: VolumeNameRepos,
			},
		}
		ss.Spec.VolumeClaimTemplates = append(ss.Spec.VolumeClaimTemplates, pvc)
		volumeClaimIndex = len(ss.Spec.VolumeClaimTemplates) - 1
	}
	ss.Spec.VolumeClaimTemplates[volumeClaimIndex] = *s.Spec.VolumeClaimTemplate.DeepCopy()
	ss.Spec.VolumeClaimTemplates[volumeClaimIndex].Name = VolumeNameRepos

	var volume *corev1.Volume
	for i := range ss.Spec.Template.Spec.Volumes {
		v := &ss.Spec.Template.Spec.Volumes[i]
		if v.Name == VolumeNameConfig {
			volume = v
			break
		}
	}
	if volume == nil {
		v := &corev1.Volume{Name: VolumeNameConfig}
		ss.Spec.Template.Spec.Volumes = append(ss.Spec.Template.Spec.Volumes, *v)
		volume = &ss.Spec.Template.Spec.Volumes[len(ss.Spec.Template.Spec.Volumes)-1]
	}
	volume.VolumeSource = corev1.VolumeSource{
		ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: s.Name,
			},
		},
	}

	var container *corev1.Container
	for i := range ss.Spec.Template.Spec.Containers {
		c := &ss.Spec.Template.Spec.Containers[i]
		if c.Name == ContainerNameSVN {
			container = c
			break
		}
	}
	if container == nil {
		ss.Spec.Template.Spec.Containers = append(ss.Spec.Template.Spec.Containers, r.svnContainerFor(s))
		container = &ss.Spec.Template.Spec.Containers[len(ss.Spec.Template.Spec.Containers)-1]
	}
	if s.Spec.PodTemplate.Image != "" {
		container.Image = s.Spec.PodTemplate.Image
	} else {
		container.Image = r.DefaultSVNServerImage
	}

	if len(s.Spec.PodTemplate.NodeSelector) > 0 {
		ss.Spec.Template.Spec.NodeSelector = map[string]string{}
		for k, v := range s.Spec.PodTemplate.NodeSelector {
			ss.Spec.Template.Spec.NodeSelector[k] = v
		}
	}
	if s.Spec.PodTemplate.ServiceAccountName != "" {
		ss.Spec.Template.Spec.ServiceAccountName = s.Spec.PodTemplate.ServiceAccountName
	}
	if len(s.Spec.PodTemplate.ImagePullSecrets) > 0 {
		ss.Spec.Template.Spec.ImagePullSecrets = make([]corev1.LocalObjectReference, len(s.Spec.PodTemplate.ImagePullSecrets))
		copy(ss.Spec.Template.Spec.ImagePullSecrets, s.Spec.PodTemplate.ImagePullSecrets)
	}
	if s.Spec.PodTemplate.Affinity != nil {
		affinity := *s.Spec.PodTemplate.Affinity
		ss.Spec.Template.Spec.Affinity = &affinity
	}
	if len(s.Spec.PodTemplate.Tolerations) > 0 {
		ss.Spec.Template.Spec.Tolerations = make([]corev1.Toleration, len(s.Spec.PodTemplate.Tolerations))
		copy(ss.Spec.Template.Spec.Tolerations, s.Spec.PodTemplate.Tolerations)
	}
}

func (r *SVNServerReconciler) svnContainerFor(s *svnv1alpha1.SVNServer) corev1.Container {
	return corev1.Container{
		Name:  ContainerNameSVN,
		Image: r.DefaultSVNServerImage,
		Ports: []corev1.ContainerPort{{
			ContainerPort: 80,
			Name:          "http",
		}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/",
					Port: intstr.FromInt(80),
				},
			},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/",
					Port: intstr.FromInt(80),
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      VolumeNameRepos,
				MountPath: VolumePathRepos,
			},
			{
				Name:      VolumeNameConfig,
				MountPath: VolumePathConfig,
			},
		},
	}
}

func (r *SVNServerReconciler) serviceFor(s *svnv1alpha1.SVNServer) (*corev1.Service, error) {
	labels := r.labelsFor(s)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.Name,
			Namespace: s.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{
				Name: "http",
				Port: 80,
			}},
			Selector:  labels,
			ClusterIP: "None",
		},
	}
	err := ctrl.SetControllerReference(s, svc, r.Scheme)
	if err != nil {
		return nil, err
	}
	return svc, nil
}

func (r *SVNServerReconciler) configMapFor(f *GeneratorFactory) (*corev1.ConfigMap, error) {
	gen := f.BuildGenerator()
	authUserFile, err := gen.AuthUserFile()
	if err != nil {
		return nil, err
	}
	authzSVNAccessFile, err := gen.AuthzSVNAccessFile()
	if err != nil {
		return nil, err
	}
	reposConfig, err := gen.ReposConfig()
	if err != nil {
		return nil, err
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.server.Name,
			Namespace: f.server.Namespace,
		},
		Data: map[string]string{
			ConfigMapKeyAuthUserFile:       authUserFile,
			ConfigMapKeyAuthzSVNAccessFile: authzSVNAccessFile,
			ConfigMapKeyRepos:              reposConfig,
		},
	}
	err = ctrl.SetControllerReference(f.server, cm, r.Scheme)
	if err != nil {
		return nil, err
	}
	return cm, nil
}

func (r *SVNServerReconciler) labelsFor(s *svnv1alpha1.SVNServer) map[string]string {
	return map[string]string{
		LabelAppKey:          LabelAppValue,
		LabelInstanceNameKey: s.Name,
	}
}

func addCondition(conds []svnv1alpha1.Condition, newCond svnv1alpha1.Condition) []svnv1alpha1.Condition {
	conds = append(conds, newCond)
	l := len(conds)
	if l <= ConditionHistoryLimit {
		return conds
	}
	sort.Slice(conds, func(ix, iy int) bool {
		// Parsing times is relatively heavy, but we can ignore this cost because
		// the number of elements in conds is very small.
		tx, ex := time.Parse(time.RFC3339, conds[ix].TransitionTime)
		ty, ey := time.Parse(time.RFC3339, conds[iy].TransitionTime)
		// We consider invalid times as earlier than any valid times.
		if ex != nil && ey != nil {
			return false
		} else if ex != nil {
			return true
		} else if ey != nil {
			return false
		}
		return tx.Before(ty)
	})

	// l-ConditionHistoryLimit > 0 is guaranteed by the above condition.
	return conds[l-ConditionHistoryLimit : l]
}

func (f *GeneratorFactory) BuildGenerator() *svnconfig.Generator {
	repos := f.BuildRepositories()
	groups := f.BuildGroups()
	users := f.BuildUsers()
	return &svnconfig.Generator{
		Repositories: repos,
		Groups:       groups,
		Users:        users,
	}
}
func (f *GeneratorFactory) BuildRepositories() []svnconfig.Repository {
	repos := make([]svnconfig.Repository, 0, len(f.repos.Items))
	for i := range f.repos.Items {
		r := f.repos.Items[i]
		perms := f.buildPermissionsOf(r.Name)
		repos = append(repos, svnconfig.Repository{Name: r.Name, Permissions: perms})
	}
	return repos
}

func (f *GeneratorFactory) buildPermissionsOf(repoName string) []svnconfig.Permission {
	perms := make([]svnconfig.Permission, 0, len(f.groups.Items))
	for i := range f.groups.Items {
		g := &f.groups.Items[i]
		for j := range g.Spec.Permissions {
			p := g.Spec.Permissions[j]
			if repoName == p.Repository {
				perms = append(perms, svnconfig.Permission{
					Group:      g.Name,
					Permission: p.Permission,
				})
			}
		}
	}
	return perms
}

func (f *GeneratorFactory) BuildGroups() []svnconfig.Group {
	groups := make([]svnconfig.Group, 0, len(f.groups.Items))
	for i := range f.groups.Items {
		g := &f.groups.Items[i]
		users := make([]string, 0, len(f.users.Items))
		for j := range f.users.Items {
			u := &f.users.Items[j]
			for k := range u.Spec.Groups {
				if g.Name == u.Spec.Groups[k].Name {
					users = append(users, u.Name)
					break
				}
			}
		}
		groups = append(groups, svnconfig.Group{
			Name:  g.Name,
			Users: users,
		})
	}
	return groups
}

func (f *GeneratorFactory) BuildUsers() []svnconfig.User {
	users := make([]svnconfig.User, 0, len(f.users.Items))
	for i := range f.users.Items {
		u := &f.users.Items[i]
		users = append(users, svnconfig.User{
			Name:              u.Name,
			EncryptedPassword: u.Spec.EncryptedPassword,
		})
	}
	return users
}

// SetupWithManager sets up the controller with the Manager.
func (r *SVNServerReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &svnv1alpha1.SVNRepository{}, IndexKeySVNServer, func(rawObj client.Object) []string {
		obj := rawObj.(*svnv1alpha1.SVNRepository)
		return []string{obj.Spec.SVNServer}
	}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &svnv1alpha1.SVNGroup{}, IndexKeySVNServer, func(rawObj client.Object) []string {
		obj := rawObj.(*svnv1alpha1.SVNGroup)
		return []string{obj.Spec.SVNServer}
	}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &svnv1alpha1.SVNUser{}, IndexKeySVNServer, func(rawObj client.Object) []string {
		obj := rawObj.(*svnv1alpha1.SVNUser)
		return []string{obj.Spec.SVNServer}
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&svnv1alpha1.SVNServer{}).
		Watches(&svnv1alpha1.SVNRepository{}, handler.EnqueueRequestsFromMapFunc(repositoryEnqueuer(mgr))).
		Watches(&svnv1alpha1.SVNGroup{}, handler.EnqueueRequestsFromMapFunc(groupEnqueuer(mgr))).
		Watches(&svnv1alpha1.SVNUser{}, handler.EnqueueRequestsFromMapFunc(userEnqueuer(mgr))).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

func repositoryEnqueuer(mgr ctrl.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		svn, ok := obj.(*svnv1alpha1.SVNRepository)
		if !ok {
			mgr.GetLogger().Info("Not an SVNRepository", "object", obj)
			return []reconcile.Request{}
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: svn.Namespace,
				Name:      svn.Spec.SVNServer,
			},
		}}
	}
}

func groupEnqueuer(mgr ctrl.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		svn, ok := obj.(*svnv1alpha1.SVNGroup)
		if !ok {
			mgr.GetLogger().Info("Not an SVNGroup", "object", obj)
			return []reconcile.Request{}
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: svn.Namespace,
				Name:      svn.Spec.SVNServer,
			},
		}}
	}
}

func userEnqueuer(mgr ctrl.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		svn, ok := obj.(*svnv1alpha1.SVNUser)
		if !ok {
			mgr.GetLogger().Info("Not an SVNUser", "object", obj)
			return []reconcile.Request{}
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: svn.Namespace,
				Name:      svn.Spec.SVNServer,
			},
		}}
	}
}
