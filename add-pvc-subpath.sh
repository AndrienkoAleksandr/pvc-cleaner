#!/bin/bash

curl -X POST http://0.0.0.0:8080/pipeline-run \
   -H "Content-Type: application/x-www-form-urlencoded" \
   -d "pipeline-run-name=nodejs-builder-2022-02-21-190636"
