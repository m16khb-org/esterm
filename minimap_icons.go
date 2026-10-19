package main

import (
	"context"
	"fmt"
	"image"
	"path/filepath"
	"strings"
)

// cdChampCircle is where CommunityDragon serves the circular portrait the game draws for a
// champion on the minimap (%[1]s: the champion's id in lower case). It is not the Data
// Dragon square: for some champions the two are different crops of the art (Qiyana's square
// scores 0.61 against the minimap icon, her circle 0.82; K'Sante's 0.84 and 0.88), and
// matching the portrait is what the tracker does.
var cdChampCircle = "https://raw.communitydragon.org/latest/game/assets/characters/%[1]s/hud/%[1]s_circle_0.png"

// mmCircleIcon returns the minimap portrait of champion champ (a livestats champion id)
// from the disk cache or CommunityDragon.
func mmCircleIcon(ctx context.Context, champ string) (image.Image, error) {
	low := strings.ToLower(champ)
	var cache string
	if root := ddCacheRoot(); root != "" {
		cache = filepath.Join(filepath.Dir(root), "cdragon", "champions", low+"_circle_0.png")
	}
	img, _, err := loadCachedImage(ctx, cache, fmt.Sprintf(cdChampCircle, low))
	return img, err
}

// mmChampIcon is the portrait the tracker matches for champion champ: its circle, and the
// Data Dragon square when there is none (an unknown folder, no network). circles tells
// whether CommunityDragon is worth asking for the next champion: one that does not answer
// at all (as opposed to one that has no such champion) is not asked again.
func mmChampIcon(ctx context.Context, ver, champ string, circles *bool) (image.Image, error) {
	if *circles {
		img, err := mmCircleIcon(ctx, champ)
		if err == nil {
			return img, nil
		}
		if !strings.Contains(err.Error(), "HTTP 404") {
			*circles = false
		}
	}
	return ddImage(ctx, ver, "img/champion/"+champ+".png")
}
