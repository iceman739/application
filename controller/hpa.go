package controller

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"strconv"

	v3 "github.com/hd-Li/types/apis/project.cattle.io/v3"
	log "github.com/sirupsen/logrus"
	yaml "gopkg.in/yaml.v2"
	"k8s.io/api/autoscaling/v2beta2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NewAutoScaleInstance Use for generate NewAutoScaleInstance
func NewAutoScaleInstance(component *v3.Component, app *v3.Application, ref *metav1.OwnerReference) v2beta2.HorizontalPodAutoscaler {
	ownerRef := GetOwnerRef(app)
	var metrics []v2beta2.MetricSpec
	matched, _ := regexp.MatchString(".*---.*---.*", component.OptTraits.Autoscaling.Metric)
	if matched {
		split := strings.Split(component.OptTraits.Autoscaling.Metric, "---")
		funcation := string(split[0])
		metric := string(split[1])
		scope := string(split[2])
		threshold := strconv.FormatInt(int64(component.OptTraits.Autoscaling.Threshold), 10)
		value := resource.MustParse(threshold)
		metrics = append(metrics, v2beta2.MetricSpec{
			Type: v2beta2.PodsMetricSourceType,
			Pods: &v2beta2.PodsMetricSource{
				Metric: v2beta2.MetricIdentifier{
					Name: fmt.Sprintf("%s_%s_%s", metric, funcation, scope),
				},
				Target: v2beta2.MetricTarget{
					Type:         v2beta2.AverageValueMetricType,
					AverageValue: &value,
				},
			},
		})
	}
	if component.OptTraits.Autoscaling.Metric == "cpu" || component.OptTraits.Autoscaling.Metric == "memory" {
		metrics = append(metrics, v2beta2.MetricSpec{
			Type: v2beta2.ResourceMetricSourceType,
			Resource: &v2beta2.ResourceMetricSource{
				Target: v2beta2.MetricTarget{
					Type:               "Utilization",
					AverageUtilization: &component.OptTraits.Autoscaling.Threshold,
				},
				Name: corev1.ResourceName(component.OptTraits.Autoscaling.Metric),
			},
		})
	}
	hpa := v2beta2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{ownerRef},
			Namespace:       app.Namespace,
			Name:            app.Name + "-" + component.Name + "-" + component.Version + "-hpa",
		},
		Spec: v2beta2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: v2beta2.CrossVersionObjectReference{
				Kind:       "Deployment",
				Name:       app.Name + "-" + component.Name + "-" + "workload" + "-" + component.Version,
				APIVersion: ref.APIVersion,
			},
			MinReplicas: &component.OptTraits.Autoscaling.MinReplicas,
			MaxReplicas: component.OptTraits.Autoscaling.MaxReplicas,
			Metrics:     metrics,
		},
	}
	return hpa
}

func (c *controller) syncHpa(component *v3.Component, app *v3.Application, ref *metav1.OwnerReference) error {
	if !(reflect.DeepEqual(component.OptTraits.Autoscaling, v3.Autoscaling{})) {
		c.syncAutoScaleConfigMap(component, app)
		c.syncAutoScale(component, app, ref)
	}
	return nil
}

func (c *controller) syncAutoScaleConfigMap(component *v3.Component, app *v3.Application) error {
	log.Infof("Sync autoscaleconfigmap for %s .......\n", app.Namespace+":"+app.Name+"-"+component.Name)
	matched, _ := regexp.MatchString(".*---.*---.*", component.OptTraits.Autoscaling.Metric)
	if matched {
		configmap, err := c.configmapLister.Get("monitoring", "adapter-config")
		if err != nil {
			if errors.IsNotFound(err) {
				log.Infoln("Configmap adapter-config not found,then create it")
				var stringmap map[string]string = make(map[string]string)
				var config MetricsDiscoveryConfig
				rule := generaterule(app.Name+"-"+component.Name+"-"+"workload", component.OptTraits.Autoscaling.Metric, component.Version)
				config.Rules = append(config.Rules, rule)
				value, err := yaml.Marshal(config)
				if err != nil {
					return err
				}
				stringmap["config.yaml"] = string(value)
				object := NewAutoScaleConfigMapObject(component, app, stringmap)
				log.Debugf("NewAutoScaleConfigMapObject %v", object)
				newconfigmap, err := c.configmapClient.Create(&object)
				if err != nil {
					log.Errorf("Create configmap for %s Error : %s\n", "adapter-config", err.Error())
					return err
				}
				log.Debugf("Create hpaconfigmap adapter-config %v", newconfigmap)
				return nil
			}
			log.Errorf("Get configmap for %s failed", "adapter-config")
			return err
		}
		var needupdate, exist bool
		config := new(MetricsDiscoveryConfig)
		if configmap != nil {
			value := configmap.Data["config.yaml"]
			log.Infof("configmap value %v", value)
			if value == "" {
				log.Infof("ConfigMap value is null")
				rule := generaterule(app.Name+"-"+component.Name+"-workload-"+component.Version, component.OptTraits.Autoscaling.Metric, app.Namespace)
				config.Rules = append(config.Rules, rule)
				needupdate = true
			} else {
				err := FromYAML(config, []byte(value))
				if err != nil {
					return err
				}
				rule := generaterule(app.Name+"-"+component.Name+"-workload-"+component.Version, component.OptTraits.Autoscaling.Metric, app.Namespace)
				if len(config.Rules) == 0 {
					log.Infoln("ConfigMap value's rule is null")
					exist = false
				} else {
					for n, i := range config.Rules {
						if i.SeriesQuery != rule.SeriesQuery {
							continue
						}
						log.Infoln("%s Check to see if an update is needed", i.SeriesQuery)
						exist = true
						if reflect.DeepEqual(i, rule) {
							log.Debugf("equal")
							needupdate = false
							break
						} else {
							log.Infoln("update rule for %s", rule.SeriesQuery)
							config.Rules[n] = rule
							needupdate = true
							break
						}
					}
				}
				if !exist {
					log.Infof("%s not exist,append it", rule.SeriesQuery)
					config.Rules = append(config.Rules, rule)
					needupdate = true
				}
			}
		}
		if !needupdate {
			return nil
		}
		value, err := yaml.Marshal(config)
		log.Infof("Config %v", config)
		if err != nil {
			return err
		}
		configmap.Data["config.yaml"] = string(value)
		newcm, err := c.configmapClient.Update(configmap)
		if err != nil {
			log.Errorf("Update configmap for %s Error : %s\n", (app.Namespace + ":" + app.Name + ":" + component.Name), err.Error())
			return err
		}
		log.Infof("Update hpaconfigmap adapter-config %v", newcm)
		return nil

	}
	return nil
}

// syncAutoScale use for syncAutoScale
func (c *controller) syncAutoScale(component *v3.Component, app *v3.Application, ref *metav1.OwnerReference) error {
	if component.OptTraits.Autoscaling.Metric == "" {
		log.Infof("This app don't need to configure autoscale for %s", app.Namespace+":"+app.Name+"-"+component.Name)
		return nil
	}
	log.Infof("Sync autoscale for %s .......\n", app.Namespace+":"+app.Name+"-"+component.Name)
	insObject := NewAutoScaleInstance(component, app, ref)
	log.Infoln(insObject)
	//zk
	insObjectString := GetObjectApplied(insObject)
	insObject.Annotations = make(map[string]string)
	insObject.Annotations[LastAppliedConfigAnnotation] = insObjectString
	instance, err := c.autoscaleLister.Get(app.Namespace, app.Name+"-"+component.Name+"-"+component.Version+"hpa")
	if err != nil {
		if errors.IsNotFound(err) {
			_, err = c.autoscaleClient.Create(&insObject)
			if err != nil {
				log.Errorf("Create autoscale for %s error : %s\n", (app.Namespace + ":" + app.Name + "-" + component.Name), err.Error())
				return nil
			}
		}
	} else {
		if instance != nil {
			if instance.Annotations[LastAppliedConfigAnnotation] != insObjectString {
				insObject.ObjectMeta.ResourceVersion = instance.ObjectMeta.ResourceVersion
				_, err = c.autoscaleClient.Update(&insObject)
				if err != nil {
					log.Errorf("Update autoscale for %s error : %s\n", (app.Namespace + ":" + app.Name + "-" + component.Name), err.Error())
				}
			}
		}
	}
	return nil
}

// NewAutoScaleConfigMapObject Use for generate NewAutoScaleConfigMapObject
func NewAutoScaleConfigMapObject(component *v3.Component, app *v3.Application, data map[string]string) corev1.ConfigMap {
	configmap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "monitoring",
			Name:      "adapter-config",
		},
		Data: data,
	}
	return configmap
}

// FromYAML use for parse FromYAML
func FromYAML(cfg *MetricsDiscoveryConfig, contents []byte) error {
	if err := yaml.UnmarshalStrict(contents, cfg); err != nil {
		return fmt.Errorf("unable to parse metrics discovery config: %v", err)
	}
	return nil
}

// generaterule use for generaterule
func generaterule(app, data, namespace string) (rule DiscoveryRule) {
	matched, _ := regexp.MatchString(".*---.*---.*", data)
	if !matched {
		return
	}
	split := strings.Split(data, "---")
	funcation := string(split[0])
	metric := string(split[1])
	scope := string(split[2])
	var rmap map[string]GroupResource = make(map[string]GroupResource)
	rmap["kubernetes_namespace"] = GroupResource{
		Resource: "namespace",
	}
	rmap["kubernetes_pod_name"] = GroupResource{
		Resource: "pod",
	}
	//rule.SeriesQuery = fmt.Sprintf(`%s{kubernetes_namespace="%s",kubernetes_pod_name=~"%s.*"}`, app.Namespace, app.Name+"-"+component.Name+"-"+"workload"+"-"+component.Version, metric)
	rule.SeriesQuery = fmt.Sprintf(`%s{kubernetes_namespace!="",kubernetes_pod_name!=""}`, metric)
	rule.Resources = ResourceMapping{
		Overrides: rmap,
	}
	rule.Name = NameMapping{
		Matches: metric,
		As:      fmt.Sprintf("${1}%s_%s_%s", metric, funcation, scope),
	}
	if scope == "all" {
		rule.MetricsQuery = fmt.Sprintf(`%s(<<.Series>>{<<.LabelMatchers>>,kubernetes_namespace="%s",kubernetes_pod_name=~"%s.*"}) by (<<.GroupBy>>)`, funcation, namespace, app)
	}
	if scope == "per" {
		rule.MetricsQuery = fmt.Sprintf(`%s(<<.Series>>{<<.LabelMatchers>>,kubernetes_namespace="%s",kubernetes_pod_name=~"%s.*"}) by (<<.GroupBy>>)`, funcation, namespace, app)
	}
	return
}
