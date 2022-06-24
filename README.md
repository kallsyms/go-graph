# go-graph

Type-aware _everything_ code search (for Go only (right now)).

See the blog post for more: [https://nickgregory.me/post/2022/06/23/go-code-as-a-graph/](https://nickgregory.me/post/2022/06/23/go-code-as-a-graph/)

## Examples

* `x` calls `y`
* `x` with type `T` is used in a call to `y` (e.g. `data uint8[]; binary.Read(..., &data)`)
* The result of a call to `x` is used as args in other functions any number of times, but eventually one result of those calls is used as an arg to function `y` (e.g. `foo := x(...); tmp1 := bar(x); tmp2 := baz(tmp1); z := y(tmp2)`


## Current sample queries

Dump all functions called:
```
FOR p IN package
FILTER p.SourceURL == "code.gitea.io/gitea"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "main"
FOR call IN OUTBOUND f Calls
FOR callee IN OUTBOUND call Callee
RETURN callee
```

Dump all statements:
```
FOR p IN package
FILTER p.SourceURL == "code.gitea.io/gitea"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "main"
FOR statement IN OUTBOUND f Statement
RETURN statement
```

Dump all call sites of a function:
```
FOR p IN package
FILTER p.SourceURL == "fmt"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "Println"
FOR callsite IN INBOUND f Callee
FOR statement IN OUTBOUND callsite CallSiteStatement
RETURN statement
```

Find all paths through a function:
```
FOR pkg IN package
FILTER pkg.SourceURL == "code.gitea.io/gitea"
FOR func IN OUTBOUND pkg Functions
FILTER func.Name == "formatBuiltWith"
FOR firststatement IN OUTBOUND func FirstStatement
FOR v, e, path IN 1..100 OUTBOUND firststatement Next
PRUNE v.isBackEdge == true
FILTER LENGTH(FOR n IN OUTBOUND v Next RETURN n) == 0
RETURN {pkg, func, vertices: CONCAT_SEPARATOR(" -> ", FOR s IN path.vertices RETURN s.Text)}
```

Dump all variables referenced in a function:
```
FOR p IN package
FILTER p.SourceURL == "code.gitea.io/gitea"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "main"
FOR statement IN OUTBOUND f Statement
FOR variable IN OUTBOUND statement References
RETURN DISTINCT variable
```

Find all uses of `encoding/binary.Read` which pass a pointer to a list (causing a slow `reflect` path to be used):
```
FOR p IN package
FILTER p.SourceURL == "encoding/binary"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "Read"
FOR callsite IN INBOUND f Callee
FOR statement IN OUTBOUND callsite CallSiteStatement
FOR var IN OUTBOUND statement References
FILTER STARTS_WITH(var.Type, "[]")
FILTER CONTAINS(statement.Text, CONCAT("&", var.Name))
FOR callfunc in INBOUND statement Statement
FOR callpkg in INBOUND callfunc Functions
RETURN {package: callpkg.SourceURL, file: statement.File, text: statement.Text, var: var.Name, type: var.Type}
```

Find all uses of `crypto/rsa.GenerateKey`, where the result flows through up to 3 intermediary variables to reach a `pem.Encode` call:
```
// find calls to crypto/rsa.GenerateKey
FOR p IN package
FILTER p.SourceURL == "crypto/rsa"
FOR f IN OUTBOUND p Functions
FILTER f.Name == "GenerateKey"
FOR call IN INBOUND f Callee
FOR srccallstmt IN OUTBOUND call CallSiteStatement

// Walk assign->ref->assign->ref->...
// until we reach a statement with an interesting call.
// v "alternates" between being a variable and being a statement

FOR v, e, path IN 1..9 OUTBOUND srccallstmt Assigns, INBOUND References
    PRUNE CONTAINS(v.Text, "Encode")
    OPTIONS {uniqueVertices: "path"}

// ensure that the end vertex is where we want
// quick check before doing any traversals
FILTER CONTAINS(v.Text, "Encode")
// now walk to the call site, called func,
// and ensure it's actually encoding/pem.Encode
FOR dstcallstmt IN INBOUND v CallSiteStatement
FOR dstcallfunc IN OUTBOUND dstcallstmt Callee
FILTER dstcallfunc.Name == "Encode"
FOR dstcallpkg IN INBOUND dstcallfunc Functions
FILTER dstcallpkg.SourceURL == "encoding/pem"

// ensure the "reference" is not actually an assignment
// go-graph considers a variable to be referenced
// even if it's on the left-hand side of an assignment
// which means `x, err := GenerateKey(); y, err := bar; Encode(y)`
// would match without this last filter since `err` is assigned
// in the first statement then also considered as "referenced"
// in the second
FILTER LENGTH(
    FOR stmt IN path.vertices
    FILTER IS_SAME_COLLECTION(statement, stmt)
    FOR checkassign IN OUTBOUND stmt Assigns
    FOR target IN path.vertices
    FILTER IS_SAME_COLLECTION(variable, target)
    FILTER POSITION(path.vertices, target, true) < POSITION(path.vertices, stmt, true)
    FILTER target == checkassign
    RETURN checkassign
) == 0
RETURN path
```
