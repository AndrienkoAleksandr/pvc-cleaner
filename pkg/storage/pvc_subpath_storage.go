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

package storage

import (
	"sync"
	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/model"
)

type PVCSubPathsStorage struct {
	subPathCache map[string]*model.PVCSubPath
	mu sync.Mutex
}

func NewPVCSubPathsStorage() *PVCSubPathsStorage {
	return &PVCSubPathsStorage{subPathCache: make(map[string]*model.PVCSubPath)}
}

func (paths *PVCSubPathsStorage) AddPVCSubPath(path *model.PVCSubPath) {
	paths.mu.Lock()
	defer paths.mu.Unlock()

	paths.subPathCache[path.PipelineRun] = path
}

func (paths *PVCSubPathsStorage) Delete(pipelinerun string) {
	paths.mu.Lock()
	defer paths.mu.Unlock()

	delete(paths.subPathCache, pipelinerun)
}

func (paths *PVCSubPathsStorage) GetAll() []*model.PVCSubPath {
	paths.mu.Lock()
	defer paths.mu.Unlock()

	subPaths := []*model.PVCSubPath{}
	for _, subPath := range paths.subPathCache {
		subPaths = append(subPaths, subPath)
	}

	return subPaths
}
