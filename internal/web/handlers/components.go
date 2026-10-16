package handlers

import (
	"fmt"
	"strings"
)

// TableActionItem represents a single item inside an action menu.
type TableActionItem struct {
	Label     string // e.g. "✏️ Edit"
	OnClick   string // JS call e.g. "openEditModal(...)"
	FormAction string // URL for POST form action (if is a form submit)
	CSRFToken string
	IsDanger  bool
	ConfirmMsg string // Confirmation prompt before submitting
}

// RenderPopoverActionMenu renders a popover-based action dropdown menu for table rows.
// Using the HTML Popover API + CSS anchor positioning guarantees that the menu
// escapes all overflow boundaries (such as overflow-x-auto, overflow-y-auto on table containers)
// and renders in the browser top-layer without clipping.
func RenderPopoverActionMenu(prefix, id string, items []TableActionItem) string {
	targetID := fmt.Sprintf("%s-menu-%s", prefix, id)
	anchorName := fmt.Sprintf("--%s-anchor-%s", prefix, id)

	var itemsHTML strings.Builder
	for _, it := range items {
		if it.FormAction != "" {
			confirmAttr := ""
			if it.ConfirmMsg != "" {
				confirmAttr = fmt.Sprintf(` onsubmit="return confirm('%s')"`, strings.ReplaceAll(it.ConfirmMsg, "'", "\\'"))
			}
			dangerClass := ""
			if it.IsDanger {
				dangerClass = "text-error "
			}
			itemsHTML.WriteString(fmt.Sprintf(`
				<li>
					<form method="POST" action="%s"%s>
						<input type="hidden" name="csrf_token" value="%s" />
						<button type="submit" class="%stext-left w-full">%s</button>
					</form>
				</li>`,
				it.FormAction, confirmAttr, it.CSRFToken, dangerClass, it.Label,
			))
		} else {
			dangerClass := ""
			if it.IsDanger {
				dangerClass = ` class="text-error"`
			}
			itemsHTML.WriteString(fmt.Sprintf(`
				<li><a%s onclick="%s">%s</a></li>`,
				dangerClass, it.OnClick, it.Label,
			))
		}
	}

	return fmt.Sprintf(`
		<button popovertarget="%s" style="anchor-name:%s" class="btn btn-ghost btn-xs">•••</button>
		<ul class="dropdown dropdown-end menu p-2 shadow-lg bg-base-100 rounded-box w-36 text-xs border border-base-300" popover id="%s" style="position-anchor:%s">
			%s
		</ul>`,
		targetID, anchorName, targetID, anchorName, itemsHTML.String(),
	)
}
