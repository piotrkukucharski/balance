package handlers

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/balance/balance/internal/web/templates"
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

type popoverActionTemplateItem struct {
	Label       string
	OnClickAttr template.JS
	HXGet       string
	FormAction  string
	CSRFToken   string
	IsDanger    bool
	ConfirmAttr template.HTMLAttr
}

type popoverMenuTemplateData struct {
	TargetID   string
	AnchorName template.CSS
	ExtraClass string
	Items      []popoverActionTemplateItem
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

	tmplItems := make([]popoverActionTemplateItem, 0, len(items))
	for _, it := range items {
		var confirmAttr template.HTMLAttr
		if it.ConfirmMsg != "" {
			escaped := strings.ReplaceAll(it.ConfirmMsg, "'", "\\'")
			confirmAttr = template.HTMLAttr(fmt.Sprintf(` onsubmit="return confirm('%s')"`, escaped))
		}
		tmplItems = append(tmplItems, popoverActionTemplateItem{
			Label:       it.Label,
			OnClickAttr: template.JS(it.OnClick),
			HXGet:       it.HXGet,
			FormAction:  it.FormAction,
			CSRFToken:   it.CSRFToken,
			IsDanger:    it.IsDanger,
			ConfirmAttr: confirmAttr,
		})
	}

	rendered, err := templates.RenderString("shared/popover_action_menu", popoverMenuTemplateData{
		TargetID:   targetID,
		AnchorName: template.CSS(anchorName),
		ExtraClass: extraClass,
		Items:      tmplItems,
	})
	if err != nil {
		return ""
	}
	return rendered
}

type sidebarNavTemplateData struct {
	BudgetID        string
	ActiveItem      string
	IsSettingsGroup bool
	ProfileLink     string
}

// RenderSidebarNavigation renders the standard left sidebar menu with subelements.
func RenderSidebarNavigation(budgetID string, activeItem string) string {
	isSettingsGroup := activeItem == "settings" || activeItem == "members" || activeItem == "tokens" || activeItem == "profile" || activeItem == "debug"

	profileLink := "/settings/profile"
	if budgetID != "" {
		profileLink = fmt.Sprintf("/budgets/%s/settings/profile", budgetID)
	}

	rendered, err := templates.RenderString("shared/sidebar_nav", sidebarNavTemplateData{
		BudgetID:        budgetID,
		ActiveItem:      activeItem,
		IsSettingsGroup: isSettingsGroup,
		ProfileLink:     profileLink,
	})
	if err != nil {
		return ""
	}
	return rendered
}

type bottomDockTemplateData struct {
	BudgetID   string
	ActiveItem string
}

// RenderBottomDock renders the mobile responsive bottom navigation bar.
func RenderBottomDock(budgetID string, activeItem string) string {
	rendered, err := templates.RenderString("shared/bottom_dock", bottomDockTemplateData{
		BudgetID:   budgetID,
		ActiveItem: activeItem,
	})
	if err != nil {
		return ""
	}
	return rendered
}
