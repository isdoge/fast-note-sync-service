package domain

import "errors"

// ErrAmbiguousPathHash requires the caller to supply a path when a hash identifies
// multiple paths. Duplicate rows for the same path keep their existing semantics.
// ErrAmbiguousPathHash 表示哈希命中多个不同路径，调用方必须补充路径；同路径重复行仍沿用原有语义。
var ErrAmbiguousPathHash = errors.New("ambiguous path hash: supply the full path")
