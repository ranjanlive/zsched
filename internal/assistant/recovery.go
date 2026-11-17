package assistant

import "encoding/json"

// repairHistory never retries interrupted actions. The persisted pending marker
// brackets an action that may have committed; inspect scheduler state and audit
// before making a new decision. Missing markers represent calls not yet started.
func repairHistory(messages []Message) []Message {
	var out []Message
	for i := 0; i < len(messages); i++ {
		m := messages[i]
		if m.Role == "tool" {
			continue
		} // discard orphaned/duplicate results
		out = append(out, m)
		if len(m.ToolCalls) == 0 {
			continue
		}
		results := map[string]Message{}
		for i+1 < len(messages) && messages[i+1].Role == "tool" {
			i++
			results[messages[i].ToolCallID] = messages[i]
		}
		for _, tc := range m.ToolCalls {
			r, exists := results[tc.ID]
			var marker struct {
				Execution string `json:"execution"`
			}
			_ = json.Unmarshal([]byte(r.Content), &marker)
			if !exists {
				r = Message{Role: "tool", ToolCallID: tc.ID, Content: `{"error":"Interrupted before this action was started. Do not automatically replay it; inspect current state first."}`}
			} else if marker.Execution == "pending" {
				r.Content = `{"error":"Interrupted while recording this action. Its side effects may have committed. Inspect current state and the audit trail; do not blindly repeat it.","execution":"uncertain"}`
			}
			out = append(out, r)
		}
	}
	return out
}
