package tool

// TodoItem is one entry of an agent's todo list.
type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// UpdateTodosArgs replaces an agent's whole todo list.
type UpdateTodosArgs struct {
	Todos []TodoItem `json:"todos"`
}

// UpdateTodos records the plan an agent is working through where the user can
// see it. Each call replaces the list.
func UpdateTodos(h Handler[UpdateTodosArgs]) Tool {
	return builtin("update_todos", "Keep a short todo list the user can see while you work. For a request with several steps, call it with every step when you start, then again whenever a step moves to in_progress or completed; keep one step in_progress at a time. Each call replaces the whole list. Skip it for a request you can finish in one or two steps.", h,
		MaxItems("todos", 30), MinLength("todos[].content", 1), Enum("todos[].status", "pending", "in_progress", "completed"))
}
