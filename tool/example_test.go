package tool_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/stevemurr/strap/tool"
)

func ExampleFunc() {
	type args struct {
		Name string `json:"name"`
	}
	parameters, err := tool.NewParameters[args](tool.MinLength("name", 1))
	if err != nil {
		panic(err)
	}
	greet := tool.Func[args]{
		Spec: tool.Definition[args]{Name: "greet", Description: "Greet someone by name.", Parameters: parameters},
		Invoke: func(_ context.Context, _ tool.Call, a args) (tool.Result, error) {
			return tool.Text("Hello, " + a.Name), nil
		},
	}
	// Heterogeneous runtime registries still use the unchanged Tool interface.
	var registered tool.Tool = greet
	result, err := registered.Call(context.Background(), tool.Call{Arguments: json.RawMessage(`{"name":"Ada"}`)})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Content.Text())
	// Output: Hello, Ada
}
