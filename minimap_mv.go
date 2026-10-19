package main

import "image"

// Glue between the pipeline and the main-view reader (mainview*.go): the camera viewport on
// the minimap and the health bars on the game picture give champion positions where the
// portraits do not (icons stacked in a teamfight, a support latched onto an ally).

// mainObs reads the main view of frame f: it finds the health bars and the camera
// viewport (on the map crop the tracker just used), matches bars to champions by level,
// health, the nameplate (learned over the game, see mainview_plate.go) and the champion's
// last position, and returns the champions' positions in map coordinates. lives carries
// the feed's level and health at the frame's instant; without it nothing is matched.
func (p *mmPipeline) mainObs(idx int, main []byte, lay *mmLayout, lives mmLives) []mvObs {
	if lives == nil || len(main) != mmMainBytes {
		return nil
	}
	img := &image.RGBA{Pix: main, Stride: mmMainW * 4, Rect: image.Rect(0, 0, mmMainW, mmMainH)}
	bars := mvFindBarsF(img, 1, mvBarFactor(lay.Name))
	if len(bars) == 0 {
		return nil
	}
	if p.cropRGBA == nil {
		p.cropRGBA = image.NewRGBA(image.Rect(0, 0, mmFrameSize, mmFrameSize))
	}
	for i, j := 0, 0; i < len(p.crop); i, j = i+3, j+4 {
		pix := p.cropRGBA.Pix[j : j+4 : j+4]
		pix[0], pix[1], pix[2], pix[3] = p.crop[i], p.crop[i+1], p.crop[i+2], 255
	}
	view := mvFindViewport(p.cropRGBA, p.mvView)
	if !view.OK {
		return nil
	}
	p.mvView = view
	champs := make([]mvChamp, 0, len(p.icons))
	for _, ic := range p.icons {
		lf, ok := lives[ic.ref.PID]
		if !ok {
			continue
		}
		c := mvChamp{PID: ic.ref.PID, Team: ic.ref.Team, Level: lf.Level, HPFrac: lf.HP, Alive: !lf.Dead}
		c.PrevX, c.PrevY, c.PrevAge, c.HasPrev = p.trk.Prev(ic.ref.PID, idx)
		champs = append(champs, c)
	}
	return mvAssign(bars, view, champs, idx, &p.mvPlates)
}
