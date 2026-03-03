package components

import (
	"fmt"

	"github.com/rivo/tview"

	"github.com/jorgerojas26/lazysql/app"
)

type PaginationState struct {
	Offset          int
	Limit           int
	VisibleRecords  int
	TotalRecords    int
	HasTotalRecords bool
	HasNextPage     bool
}

type Pagination struct {
	*tview.Flex
	state    *PaginationState
	textView *tview.TextView
}

func NewPagination() *Pagination {
	wrapper := tview.NewFlex()
	wrapper.SetBorderPadding(0, 0, 1, 1)
	wrapper.SetBorder(true)

	textView := tview.NewTextView()
	textView.SetText("0-0 rows")
	textView.SetTextAlign(tview.AlignCenter)

	wrapper.AddItem(textView, 0, 1, false)

	return &Pagination{
		Flex:     wrapper,
		textView: textView,
		state: &PaginationState{
			Offset:          0,
			Limit:           app.App.Config().DefaultPageSize,
			VisibleRecords:  0,
			TotalRecords:    0,
			HasTotalRecords: false,
			HasNextPage:     false,
		},
	}
}

func (pagination *Pagination) GetOffset() int {
	return pagination.state.Offset
}

func (pagination *Pagination) GetTotalRecords() int {
	return pagination.state.TotalRecords
}

func (pagination *Pagination) GetLimit() int {
	return pagination.state.Limit
}

func (pagination *Pagination) GetIsFirstPage() bool {
	return pagination.state.Offset == 0
}

func (pagination *Pagination) GetIsLastPage() bool {
	return !pagination.state.HasNextPage
}

func (pagination *Pagination) SetPageStats(visibleRecords int, hasNextPage bool) {
	pagination.state.VisibleRecords = visibleRecords
	pagination.state.HasNextPage = hasNextPage

	if pagination.state.HasTotalRecords {
		// If the page fetch returned no rows, always stop forward paging.
		// This prevents stale manual counts from causing endless "next page" fetches.
		if visibleRecords == 0 {
			pagination.state.HasNextPage = false
		} else {
			pagination.state.HasNextPage = pagination.state.Offset+visibleRecords < pagination.state.TotalRecords
		}
	}

	pagination.updateText()
}

func (pagination *Pagination) SetTotalRecords(total int) {
	pagination.state.TotalRecords = total
	pagination.state.HasTotalRecords = true
	pagination.state.HasNextPage = pagination.state.Offset+pagination.state.VisibleRecords < total
	pagination.updateText()
}

func (pagination *Pagination) ClearTotalRecords() {
	pagination.state.HasTotalRecords = false
	pagination.state.TotalRecords = 0
	pagination.updateText()
}

func (pagination *Pagination) SetLimit(limit int) {
	pagination.state.Limit = limit
	pagination.updateText()
}

func (pagination *Pagination) SetOffset(offset int) {
	pagination.state.Offset = offset
	pagination.updateText()
}

func (pagination *Pagination) updateText() {
	start := 0
	end := 0

	if pagination.state.VisibleRecords > 0 {
		start = pagination.state.Offset + 1
		end = pagination.state.Offset + pagination.state.VisibleRecords
	}

	if pagination.state.HasTotalRecords {
		if end > pagination.state.TotalRecords {
			end = pagination.state.TotalRecords
		}
		pagination.textView.SetText(fmt.Sprintf("%d-%d of %d rows", start, end, pagination.state.TotalRecords))
		return
	}

	pagination.textView.SetText(fmt.Sprintf("%d-%d rows", start, end))
}
