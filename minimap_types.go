package main

// Shared types for the broadcast-minimap pipeline:
// video (minimap_video.go) -> layout/HUD (minimap_layout.go) -> map fit (minimap_fit.go)
// -> tracking (minimap_detect.go) -> rendering (minimap_render.go).

// mmFrameSize is the side length of every minimap frame handed to the detector: the map
// box is resampled from the minimap region to mmFrameSize×mmFrameSize rgb24.
const mmFrameSize = 288

// mmBox is a rectangle in 1920×1080 reference coordinates. A map box covers exactly the
// map image (same extent as Data Dragon map11.png), so normalized positions inside it
// map linearly onto Summoner's Rift game units.
type mmBox struct{ X, Y, W, H int }

// mmRegionMM and mmRegionHUD are the two fixed crops of the 1920×1080 broadcast frame
// the video pipeline hands to Go: the minimap with its border (every league's map sits
// inside it) and the scoreboard strip with the game clock and both kill counters.
var (
	mmRegionMM  = mmBox{1488, 648, 432, 432}
	mmRegionHUD = mmBox{840, 0, 240, 112}
)

// mmFrame is one decoded video frame. Idx is the frame index on the stream timeline
// (Idx/fps seconds since the pipeline started); it increases monotonically, gaps allowed.
type mmFrame struct {
	Idx     int
	Restart bool   // first frame of a restarted stream: its timeline is not continuous with the frames before
	MM      []byte // mmRegionMM.W*H*3 rgb24
	HUD     []byte // mmRegionHUD.W*H*3 rgb24
	// Main is the main view (mmMainBytes of rgba, see mmRegionFilterMain) of the newest main
	// frame that fell due with this frame; nil on frames without one.
	Main []byte
}

// mmLayout is one broadcast overlay (league): nominal boxes in 1920×1080 reference
// coordinates. Map covers exactly the map image; the others bound the text.
type mmLayout struct {
	Name                            string // "worlds" | "lck" | "lpl" | "lec"
	Map, Clock, KillsBlue, KillsRed mmBox
}

// mmHUD is the scoreboard reading of one frame. Clock and kills are independent: a frame
// can show the clock without the counters (and the other way round) while overlays slide
// in. Fields of an unreadable element are zero.
type mmHUD struct {
	ClockSec int
	ClockOK  bool
	Blue     int
	Red      int
	KillsOK  bool
}

// champRef identifies one champion in the current game (from livestats gameMetadata).
type champRef struct {
	PID   int    // livestats participantId (1..10)
	Team  int    // 100 blue, 200 red
	Champ string // Data Dragon champion id, e.g. "LeeSin"
	Role  string // livestats role: top, jungle, mid, bottom, support
}

// mmDet is the tracker output for one champion on one frame.
// X, Y are normalized to the minimap square (0..1, origin top-left).
type mmDet struct {
	champRef
	X, Y    float64
	Score   float32
	Visible bool // false: not found on this frame (dead, hidden, or minimap absent)
	// Age is how many frames the champion has been kept at its last seen position because
	// the tracker lost it (0: found on this frame); only meaningful while Visible.
	Age int
	// Covered: not found on this frame, but its estimated position lies under another found
	// icon, which hides it; Age stays 0 then. The position is inferred, not seen.
	Covered bool
	// MV: measured on this frame from the main view's health bar, not from the portrait.
	MV bool
	// Attached: the champion is latched onto the ally whose icon it is drawn at (a support
	// has no icon of its own then): Covered too, and it is as fresh as that ally's position.
	Attached bool
}

// mmIconState is how a champion marker is drawn.
type mmIconState int

const (
	mmIconLive  mmIconState = iota // tracked, or lost for a moment: the position is current
	mmIconStale                    // the tracker lost it: its last seen position, drawn dimmed
	mmIconDead                     // dead: at its team's base, greyed, with the respawn countdown
)

// mmMarker is one champion marker to draw on the terminal minimap.
type mmMarker struct {
	Team  int
	Label string  // 2 runes
	X, Y  float64 // normalized 0..1, origin top-left
	State mmIconState
	Secs  int // mmIconStale: seconds since it was last seen; mmIconDead: seconds until the respawn
}

// mmLife is what the feed says about one champion at the shown instant.
type mmLife struct {
	Dead  bool
	Secs  float64 // while Dead: seconds until the respawn, 0 when it is due
	Exact bool    // Secs is the feed's own respawn time (replay), not the death-timer estimate
	// AliveFor is how long the champion has been alive, in seconds (since its respawn, or
	// since the game started). A sighting older than that is from before the champion died
	// and does not count.
	AliveFor float64
	// Level and HP (current health / maximum, 0 when unknown) as the feed's frame at the
	// shown instant has them: the main-view reader matches health bars to champions by them.
	Level int
	HP    float64
}

// mmLives maps livestats participant ids to their life at the shown instant. A nil map
// means nothing is known (no feed to ask): the minimap then shows only what the tracker
// found within the last mmHoldShort frames.
type mmLives map[int]mmLife
