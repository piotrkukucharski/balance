package handlers

import (
	"fmt"
	"strings"
)

// TableActionItem represents a single item inside an action menu.
type TableActionItem struct {
	Label      string // e.g. "✏️ Edit"
	OnClick    string // JS call e.g. "openEditModal(...)"
	HXGet      string // e.g. "/budgets/.../history/..."
	FormAction string // URL for POST form action (if is a form submit)
	CSRFToken  string
	IsDanger   bool
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
		} else if it.HXGet != "" {
			dangerClass := ""
			if it.IsDanger {
				dangerClass = ` class="text-error"`
			}
			itemsHTML.WriteString(fmt.Sprintf(`
				<li><button type="button"%s hx-get="%s" hx-target="body" hx-swap="beforeend">%s</button></li>`,
				dangerClass, it.HXGet, it.Label,
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

// RenderSidebarNavigation renders the standard left sidebar menu with subelements.
func RenderSidebarNavigation(budgetID string, activeItem string) string {
	itemClass := func(item string) string {
		if item == activeItem {
			return ` class="active font-bold"`
		}
		return ""
	}

	settingsOpen := " open"
	settingsSummaryClass := ""
	if activeItem == "settings" || activeItem == "members" || activeItem == "tokens" {
		settingsSummaryClass = ` class="font-bold text-primary"`
	}

	return fmt.Sprintf(`
            <ul class="menu menu-md w-full p-0 gap-1">
                <li><a href="/budgets/%s/budget"%s>📂 1. Budget (Buckets)</a></li>
                <li><a href="/budgets/%s/accounts"%s>💳 2. Accounts</a></li>
                <li><a href="/budgets/%s/transactions"%s>💸 3. Transactions</a></li>
                <li><a href="/budgets/%s/reports"%s>📊 4. Reports</a></li>
                <li><a href="/budgets/%s/parties"%s>🏢 5. External Parties</a></li>
                <li>
                    <details%s>
                        <summary%s>
                            <a href="/budgets/%s/settings" class="grow">⚙️ 6. Settings</a>
                        </summary>
                        <ul class="before:hidden">
                            <li><a href="/budgets/%s/settings"%s>General</a></li>
                            <li><a href="/budgets/%s/settings/members"%s>👥 Members</a></li>
                            <li><a href="/budgets/%s/settings/tokens"%s>🔑 API Tokens</a></li>
                        </ul>
                    </details>
                </li>
            </ul>`,
		budgetID, itemClass("budget"),
		budgetID, itemClass("accounts"),
		budgetID, itemClass("transactions"),
		budgetID, itemClass("reports"),
		budgetID, itemClass("parties"),
		settingsOpen, settingsSummaryClass,
		budgetID,
		budgetID, itemClass("settings"),
		budgetID, itemClass("members"),
		budgetID, itemClass("tokens"),
	)
}

// RenderBottomDock renders the mobile responsive bottom navigation bar.
func RenderBottomDock(budgetID string, activeItem string) string {
	dockClass := func(item string) string {
		if item == activeItem {
			return ` class="dock-active"`
		}
		return ""
	}

	return fmt.Sprintf(`
    <div class="dock md:hidden bg-base-100 border-t border-base-300">
        <a href="/budgets/%s/budget"%s>
            <span class="text-xl">📂</span>
            <span class="dock-label">Budget</span>
        </a>
        <a href="/budgets/%s/accounts"%s>
            <span class="text-xl">💳</span>
            <span class="dock-label">Accounts</span>
        </a>
        <a href="/budgets/%s/transactions"%s>
            <span class="text-xl">💸</span>
            <span class="dock-label">Txns</span>
        </a>
        <a href="/budgets/%s/reports"%s>
            <span class="text-xl">📊</span>
            <span class="dock-label">Reports</span>
        </a>
        <a href="/budgets/%s/parties"%s>
            <span class="text-xl">🏢</span>
            <span class="dock-label">Parties</span>
        </a>
        <a href="/budgets/%s/settings"%s>
            <span class="text-xl">⚙️</span>
            <span class="dock-label">Settings</span>
        </a>
    </div>`,
		budgetID, dockClass("budget"),
		budgetID, dockClass("accounts"),
		budgetID, dockClass("transactions"),
		budgetID, dockClass("reports"),
		budgetID, dockClass("parties"),
		budgetID, dockClass("settings"),
	)
}

