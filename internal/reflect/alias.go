package reflect

import "reflect"

// Type aliases reflect.Type.
type Type = reflect.Type

// Ptr is reflect.Ptr.
const Ptr = reflect.Ptr

// Standard reflect function aliases.
var (
	Append      = reflect.Append
	AppendSlice = reflect.AppendSlice
	ChanOf      = reflect.ChanOf
	CopySlice   = reflect.Copy
	DeepEqual   = reflect.DeepEqual
	Indirect    = reflect.Indirect
	MakeChan    = reflect.MakeChan
	MakeFunc    = reflect.MakeFunc
	MakeMap     = reflect.MakeMap
	MakeSlice   = reflect.MakeSlice
	MapOf       = reflect.MapOf
	New         = reflect.New
	NewAt       = reflect.NewAt
	PtrTo       = reflect.PtrTo
	Select      = reflect.Select
	SliceOf     = reflect.SliceOf
	TypeOf      = reflect.TypeOf
	ValueOf     = reflect.ValueOf
	Zero        = reflect.Zero
)
