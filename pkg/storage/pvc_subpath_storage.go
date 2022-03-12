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
	"database/sql"
	"os"
	"sync"

	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/model"
	_ "github.com/mattn/go-sqlite3"
)

const (
	defaultDBPath = "/workspace/source/database/foo.db"

	initialDB = `CREATE TABLE IF NOT EXISTS 'pvcsubpath' (
	'uid' INTEGER PRIMARY KEY AUTOINCREMENT, 
	'pipelinerun' VARCHAR(64) NULL, 
	'pvcsubpath' VARCHAR(64) NULL
	);`
)

type PVCSubPathsStorage struct {
	db *sql.DB
	mu sync.Mutex
}

func NewPVCSubPathsStorage() *PVCSubPathsStorage {
	return &PVCSubPathsStorage{}
}

func (paths *PVCSubPathsStorage) Init() error {
	dbPath := os.Getenv("DB_PATH")

	if len(dbPath) == 0 {
		dbPath = defaultDBPath
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return err
	}

	_, err = db.Exec(initialDB)
	if err != nil {
		return err
	}

	paths.db = db
	return nil
}

func (paths *PVCSubPathsStorage) AddPVCSubPath(path *model.PVCSubPath) error {
	paths.mu.Lock()
	defer paths.mu.Unlock()

	stmt, err := paths.db.Prepare("INSERT INTO pvcsubpath(pipelinerun, pvcsubpath) values(?,?)")
	if err != nil {
		return err
	}

	res, err := stmt.Exec(path.PipelineRun, path.PVCSubPath)
	if err != nil {
		return err
	}

	_, err = res.LastInsertId()
	if err != nil {
		return err
	}

	return nil
}

func (paths *PVCSubPathsStorage) Delete(pipelinerun string) error {
	paths.mu.Lock()
	defer paths.mu.Unlock()

	stmt, err := paths.db.Prepare("delete from pvcsubpath where pipelinerun=?")
	if err != nil {
		return err
	}

	res, err := stmt.Exec(pipelinerun)
	if err != nil {
		return err
	}

	_, err = res.RowsAffected()
	if err != nil {
		return err
	}

	return nil
}

func (paths *PVCSubPathsStorage) GetAll() ([]*model.PVCSubPath, error) {
	paths.mu.Lock()
	defer paths.mu.Unlock()

	rows, err := paths.db.Query("SELECT * FROM pvcsubpath")
	if err != nil {
		return []*model.PVCSubPath{}, err
	}

	defer rows.Close() // good habit to close

	var uid int
	var pipelinerun string
	var pvcsubpath string

	var subPaths []*model.PVCSubPath
	for rows.Next() {
		if err = rows.Scan(&uid, &pipelinerun, &pvcsubpath); err != nil {
			return []*model.PVCSubPath{}, err
		}

		pvcsubpath := &model.PVCSubPath{PipelineRun: pipelinerun, PVCSubPath: pvcsubpath}
		subPaths = append(subPaths, pvcsubpath)
	}

	return subPaths, nil
}

func (paths *PVCSubPathsStorage) Close() error {
	return paths.db.Close()
}
