### PVC cleaner component

This component does cleanup pvc subfolders for Tekton pipelineruns.
Pipelineruns stores source code in the subfolders inside parent directory /workspace/source.
Example file tree:

```
/workspace
        |  —- source
                |
                | —- pv-devfile-build-2022-03-02-134823
                |                   |
                |                   | — some/source/code
                |
                | — pv-devfile-build-2022-03-02-134828
                |                   |
                |                   | — some/source/code
                |
                …
```
