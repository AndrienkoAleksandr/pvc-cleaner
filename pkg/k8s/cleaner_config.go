//
// Copyright 2022 Red Hat, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package k8s

import (
	"context"
	"fmt"
	"log"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	configMapName                 = "pvc-cleaner-config"
	addPipelineRunResourceVersion = "add-pipeline-resource-version"
)

type PVCCleanerConfig struct {
	K8sClient *kubernetes.Clientset
	namespace string
}

func NewCleanerConfig(k8sClient *kubernetes.Clientset, namespace string) *PVCCleanerConfig {
	return &PVCCleanerConfig{
		K8sClient: k8sClient,
		namespace: namespace,
	}
}

func (cc *PVCCleanerConfig) CreateIfNotPresent() {
	api := cc.K8sClient.CoreV1().ConfigMaps(cc.namespace)

	_, err := api.Get(context.TODO(), configMapName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			cm := cc.getDefaultConfig()
			log.Println("Create configmap with default configuration")

			_, err = api.Create(context.TODO(), cm, metav1.CreateOptions{})
			if err != nil {
				log.Fatal(err)
			}
		} else {
			log.Println(err)
		}
	}
}

func (cc *PVCCleanerConfig) UpdateWatchResourceVersion(resourceVersion string) error {
	api := cc.K8sClient.CoreV1().ConfigMaps(cc.namespace)

	patchTemplate := `{"data": {"%s": "%s"}}`
	patch := fmt.Sprintf(patchTemplate, addPipelineRunResourceVersion, resourceVersion)

	if _, err := api.Patch(context.TODO(), configMapName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return err
	}

	return nil
}

func (cc *PVCCleanerConfig) GetWatchResourceVersion() (string, error) {
	api := cc.K8sClient.CoreV1().ConfigMaps(cc.namespace)

	cm, err := api.Get(context.TODO(), configMapName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	resourceVersion := cm.Data[addPipelineRunResourceVersion]

	return resourceVersion, nil
}

func (cc *PVCCleanerConfig) getDefaultConfig() *corev1.ConfigMap {
	data := make(map[string]string)
	data[addPipelineRunResourceVersion] = "1"

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: cc.namespace,
		},
		Data: data,
	}
}
