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

package pkg

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/homedir"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	kubeconfig *string
)

func ParseFlags() {
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}

	flag.Parse()
}

func GetClusterConfigPath() string {
	return *kubeconfig
}

func IsOutSideClusterConfig() bool {
	isOutSideClusterConfig := os.Getenv("OUTSIDE_CLUSTER")
	return strings.ToLower(isOutSideClusterConfig) == "true"
}

func IsNamespaceInDeletingState(clientset *kubernetes.Clientset, namespaceName string) (bool, error) {
	namespace, err := clientset.CoreV1().Namespaces().Get(context.TODO(), namespaceName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}

	if !namespace.ObjectMeta.DeletionTimestamp.IsZero() {
		return true, nil
	}
	return false, nil
}