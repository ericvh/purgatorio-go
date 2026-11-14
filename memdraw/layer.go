package memdraw

// Refreshfn repaints r of a window whose obscured parts are not saved.
type Refreshfn func(i *Image, r Rectangle, arg any)

// Screen is a Memscreen: a stack of windows on an image.
type Screen struct {
	Frontmost *Image // frontmost layer on screen
	Rearmost  *Image // rearmost layer on screen
	Image     *Image // upon which all layers are drawn
	Fill      *Image // if non-nil, picture to use when repainting
}

// Layer is a Memlayer: the window state of an image on a screen.
type Layer struct {
	Screenr    Rectangle // true position of layer on screen
	Delta      Point     // add delta to go from image coords to screen
	Screen     *Screen
	Front      *Image // window in front of this one
	Rear       *Image // window behind this one
	Clear      bool   // layer is fully visible
	Save       *Image // save area for obscured parts
	Refreshfn  Refreshfn
	Refreshptr any
}
