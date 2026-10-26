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
func RenderPopoverActionMenu(prefix, id string, items []TableActionItem, extraClasses ...string) string {
	extraClass := ""
	if len(extraClasses) > 0 && extraClasses[0] != "" {
		extraClass = " " + extraClasses[0]
	}
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
		<button popovertarget="%s" style="anchor-name:%s" class="btn btn-ghost btn-sm sm:btn-xs min-h-8 sm:min-h-6 px-2 touch-manipulation" aria-label="More actions">•••</button>
		<ul class="dropdown dropdown-end menu p-2 shadow-lg bg-base-100 rounded-box w-40 text-xs border border-base-300%s" popover id="%s" style="position-anchor:%s; position-try-fallbacks: flip-block, --flip-block; margin: 0;" onbeforetoggle="if(event.newState==='open'){const r=this.getBoundingClientRect();if(window.innerHeight-r.bottom<120){this.classList.add('dropdown-top');}}else{this.classList.remove('dropdown-top');}">
			%s
		</ul>`,
		targetID, anchorName, extraClass, targetID, anchorName, itemsHTML.String(),
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
	if activeItem == "settings" || activeItem == "members" || activeItem == "tokens" || activeItem == "profile" || activeItem == "debug" {
		settingsSummaryClass = ` class="font-bold text-primary"`
	}

	profileLink := "/settings/profile"
	if budgetID != "" {
		profileLink = fmt.Sprintf("/budgets/%s/settings/profile", budgetID)
	}

	debugLinkHTML := ""
	if budgetID != "" {
		debugClass := ""
		if activeItem == "debug" {
			debugClass = ` class="active font-bold"`
		}
		debugLinkHTML = fmt.Sprintf(`<li><a href="/budgets/%s/debug"%s>🛠️ Diagnostic Tools (Debug)</a></li>`, budgetID, debugClass)
	}

	return fmt.Sprintf(`
            <ul class="menu menu-md w-full p-0 gap-1">
                <li><a href="/budgets/%s/budget"%s>📂 1. Budget (Buckets)</a></li>
                <li><a href="/budgets/%s/accounts"%s>💳 2. Accounts</a></li>
                <li><a href="/budgets/%s/transactions"%s>💸 3. Transactions</a></li>
                <li><a href="/budgets/%s/automations"%s>⚡ 4. Automations</a></li>
                <li><a href="/budgets/%s/reports"%s>📊 5. Reports</a></li>
                <li><a href="/budgets/%s/parties"%s>🏢 6. External Parties</a></li>
                <li>
                    <details%s>
                        <summary%s>
                            <a href="/budgets/%s/settings" class="grow">⚙️ 7. Settings</a>
                        </summary>
                        <ul class="before:hidden">
                            <li><a href="/budgets/%s/settings"%s>General</a></li>
                            <li><a href="/budgets/%s/settings/members"%s>👥 Members</a></li>
                            <li><a href="/budgets/%s/settings/tokens"%s>🔑 API Tokens</a></li>
                            <li><a href="%s"%s>👤 Profile & Preferences</a></li>
                            %s
                        </ul>
                    </details>
                </li>
            </ul>`,
		budgetID, itemClass("budget"),
		budgetID, itemClass("accounts"),
		budgetID, itemClass("transactions"),
		budgetID, itemClass("automations"),
		budgetID, itemClass("reports"),
		budgetID, itemClass("parties"),
		settingsOpen, settingsSummaryClass,
		budgetID,
		budgetID, itemClass("settings"),
		budgetID, itemClass("members"),
		budgetID, itemClass("tokens"),
		profileLink, itemClass("profile"),
		debugLinkHTML,
	)
}

// RenderBottomDock renders the mobile responsive bottom navigation bar.
func RenderBottomDock(budgetID string, activeItem string) string {
	dockClass := func(item string) string {
		if item == activeItem {
			return ` class="dock-active text-primary font-semibold touch-manipulation min-h-12"`
		}
		return ` class="touch-manipulation min-h-12 text-base-content/70"`
	}

	return fmt.Sprintf(`
    <div class="dock dock-md md:hidden bg-base-100/95 backdrop-blur-xs border-t border-base-300 z-40 pb-[env(safe-area-inset-bottom)] shadow-lg" role="navigation" aria-label="Mobile Bottom Navigation">
        <a href="/budgets/%s/budget"%s>
            <span class="text-xl leading-none">📂</span>
            <span class="dock-label text-[11px]">Budget</span>
        </a>
        <a href="/budgets/%s/accounts"%s>
            <span class="text-xl leading-none">💳</span>
            <span class="dock-label text-[11px]">Accounts</span>
        </a>
        <a href="/budgets/%s/transactions"%s>
            <span class="text-xl leading-none">💸</span>
            <span class="dock-label text-[11px]">Txns</span>
        </a>
        <a href="/budgets/%s/automations"%s>
            <span class="text-xl leading-none">⚡</span>
            <span class="dock-label text-[11px]">Rules</span>
        </a>
        <a href="/budgets/%s/reports"%s>
            <span class="text-xl leading-none">📊</span>
            <span class="dock-label text-[11px]">Reports</span>
        </a>
        <a href="/budgets/%s/settings"%s>
            <span class="text-xl leading-none">⚙️</span>
            <span class="dock-label text-[11px]">Settings</span>
        </a>
    </div>`,
		budgetID, dockClass("budget"),
		budgetID, dockClass("accounts"),
		budgetID, dockClass("transactions"),
		budgetID, dockClass("automations"),
		budgetID, dockClass("reports"),
		budgetID, dockClass("settings"),
	)
}

