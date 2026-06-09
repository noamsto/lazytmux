package main

const (
	targetCellWidth = 20 // desired thumbnail width in cells
	targetCellRows  = 5  // desired rows per screenful before paging
	labelRows       = 1  // one label line per cell
)

// grid is the computed layout for a given pane size and image count.
type grid struct {
	cols, rows   int // visible columns / rows of cells
	cellW, cellH int // cell box in cells (cellH includes the label row)
	imgH         int // image rows inside a cell (cellH - labelRows)
	perPage      int
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// computeGrid derives the grid layout. paneH-1 reserves the bottom status row.
func computeGrid(paneW, paneH, imageCount int) grid {
	cols := clamp(paneW/targetCellWidth, 1, maxCellDim)
	body := paneH - 1
	rows := clamp(targetCellRows, 1, maxRowsThatFit(body))
	cellW := clamp(paneW/cols, 1, maxCellDim)
	cellH := clamp(body/rows, labelRows+1, maxCellDim+labelRows)
	imgH := clamp(cellH-labelRows, 1, maxCellDim)
	return grid{cols: cols, rows: rows, cellW: cellW, cellH: cellH, imgH: imgH, perPage: cols * rows}
}

// maxRowsThatFit caps rows so each cell keeps at least a label row + 1 image row.
func maxRowsThatFit(body int) int {
	if body < labelRows+1 {
		return 1
	}
	return body / (labelRows + 1)
}

func pageOf(index, perPage int) int { return index / perPage }

func pageCount(n, perPage int) int {
	if n <= 0 {
		return 1
	}
	return (n + perPage - 1) / perPage
}

// moveCursor shifts the selected index by delta, clamped to [0, count-1].
func moveCursor(index, delta, count int) int {
	if count == 0 {
		return 0
	}
	return clamp(index+delta, 0, count-1)
}
