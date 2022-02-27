package k8s

import (
	"io/ioutil"
)

var (
	namespace string
)

const (
	namespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// GetNamespace returns namespace where is located pod deployment.
func GetNamespace() (string, error) {
	if len(namespace) == 0 {
		namespaceBytes, err := ioutil.ReadFile(namespaceFile)
		if err != nil {
			return "", err
		}
		namespace = string(namespaceBytes)
	}

	return namespace, nil
}