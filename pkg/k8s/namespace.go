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
	"io/ioutil"
	"os"
)

var (
	namespace string
)

const (
	namespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// GetNamespace returns namespace where is located pod deployment.
func GetNamespace() (string, error) {
	debug := os.Getenv("DEBUG")
	if debug == "true" {
		return "pvc-cleaner", nil
	}
	if len(namespace) == 0 {
		namespaceBytes, err := ioutil.ReadFile(namespaceFile)
		if err != nil {
			return "", err
		}
		namespace = string(namespaceBytes)
	}

	return namespace, nil
}
