package session

import (
	"encoding/json"
	"slices"
)

// TodoLink says which subagents and background tasks work for the todo with this content. A todo has no id of its own:
// TodoWrite replaces the whole list, so its content is what identifies it.
type TodoLink struct {
	Content  string   `json:"content"`
	AgentIDs []string `json:"agentIds,omitempty"`
	TaskIDs  []string `json:"taskIds,omitempty"`
}

// TodoLinkFold rebuilds the links of a todo list from a branch of the transcript, in its order: each TodoWrite input
// (Replace) and each todo_link entry (Add). Links go by content, so the result does not depend on how the calls of one
// assistant message interleaved: a link to an item the list no longer has is dropped, as it was live.
type TodoLinkFold struct {
	contents map[string]bool
	links    []TodoLink
}

// Replace applies the todos of a TodoWrite input: links of items whose content is gone are dropped.
func (f *TodoLinkFold) Replace(todos json.RawMessage) {
	var items []struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(todos, &items) != nil {
		return
	}
	f.contents = make(map[string]bool, len(items))
	for _, t := range items {
		f.contents[t.Content] = true
	}
	f.links = slices.DeleteFunc(f.links, func(l TodoLink) bool { return !f.contents[l.Content] })
}

// Add applies a recorded link.
func (f *TodoLinkFold) Add(l TodoLink) {
	if !f.contents[l.Content] {
		return
	}
	i := slices.IndexFunc(f.links, func(o TodoLink) bool { return o.Content == l.Content })
	if i < 0 {
		f.links = append(f.links, TodoLink{Content: l.Content})
		i = len(f.links) - 1
	}
	f.links[i].AgentIDs = MergeIDs(f.links[i].AgentIDs, l.AgentIDs)
	f.links[i].TaskIDs = MergeIDs(f.links[i].TaskIDs, l.TaskIDs)
}

// Links returns the links now on record.
func (f *TodoLinkFold) Links() []TodoLink { return slices.Clone(f.links) }

// MergeIDs returns to with the ids it lacks added. to itself is not changed: lists already sent out may share it.
func MergeIDs(to, ids []string) []string {
	for _, id := range ids {
		if !slices.Contains(to, id) {
			to = append(slices.Clip(to), id)
		}
	}
	return to
}
