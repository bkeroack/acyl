# Deprecated Interface Mocks

These are deprecated and no additional should be added.

If you need to update any for old legacy tests, run:

```bash
# For example:
$ GO111MODULE=off mockgen -package mocks github.com/bkeroack/acyl/pkg/persistence DataLayer > ./mock_datalayer.go
```
