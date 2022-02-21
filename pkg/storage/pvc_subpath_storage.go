package storage

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/AndrienkoAleksandr/pvc-cleaner/pkg/model"
	_ "github.com/mattn/go-sqlite3"
)

const defaultDBPath = "/workspace/source/database/foo.db"

type PVCSubPathsStorage struct {
	db *sql.DB
}

func NewPVCSubPathsStorage() (*PVCSubPathsStorage) {
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

	result, err := db.Exec("CREATE TABLE IF NOT EXISTS 'pvcsubpath' ('uid' INTEGER PRIMARY KEY AUTOINCREMENT, 'pipelinerun' VARCHAR(64) NULL, 'pvcsubpath' VARCHAR(64) NULL);")
	if err != nil {
		return err
	}
	fmt.Println(result)

	paths.db = db
	return nil
}

func (paths *PVCSubPathsStorage) AddPVCSubPath(path *model.PVCSubPath) error {
	stmt, err := paths.db.Prepare("INSERT INTO pvcsubpath(pipelinerun, pvcsubpath) values(?,?)")
	if err != nil {
		return err
	}

	res, err := stmt.Exec(path.PipelineRun, path.PVCSubPath)
	if err != nil {
		return err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return err
	}

	fmt.Println(id)

	return nil
}

func (paths *PVCSubPathsStorage) Delete(pipelinerun string) error {
	stmt, err := paths.db.Prepare("delete from userinfo where pipelinerun=?")
	if err != nil {
		return err
	}

	res, err := stmt.Exec(pipelinerun)
	if err != nil {
		return err
	}

	affect, err := res.RowsAffected()
	if err != nil {
		return err
	}
	fmt.Println(affect)

	return nil
}

func (paths *PVCSubPathsStorage) GetAll() ([]*model.PVCSubPath, error) {
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

		fmt.Println(uid)
		fmt.Println(pipelinerun)
		fmt.Println(pvcsubpath)

		pvcsubpath := &model.PVCSubPath{PipelineRun: pipelinerun, PVCSubPath: pvcsubpath}
		subPaths = append(subPaths, pvcsubpath)
	}

	return subPaths, nil
}

func (paths *PVCSubPathsStorage) Close() error {
	return paths.db.Close()
}
