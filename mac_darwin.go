//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit -framework QuartzCore
#import <AppKit/AppKit.h>
#import <QuartzCore/QuartzCore.h>

void serialToolSetDockVisible(int visible) {
	if (visible) {
		[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
		[NSApp activateIgnoringOtherApps:YES];
	} else {
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
	}
}

static NSString *const kSerialToolTrafficBacking = @"serialToolTrafficBacking";

static void clearTrafficBacking(CALayer *layer) {
	NSArray<CALayer *> *subs = [layer.sublayers copy];
	for (CALayer *sub in subs) {
		if ([sub.name isEqualToString:kSerialToolTrafficBacking]) {
			[sub removeFromSuperlayer];
		}
	}
}

void serialToolSetTrafficLightsFocused(void *window, int focused) {
	NSWindow *nsWindow = (__bridge NSWindow *)window;
	if (!nsWindow) {
		return;
	}
	NSWindowButton kinds[3] = {
		NSWindowCloseButton,
		NSWindowMiniaturizeButton,
		NSWindowZoomButton,
	};
	for (int i = 0; i < 3; i++) {
		NSButton *button = [nsWindow standardWindowButton:kinds[i]];
		if (!button) {
			continue;
		}
		button.wantsLayer = YES;
		CALayer *layer = button.layer;
		if (!layer) {
			continue;
		}
		clearTrafficBacking(layer);
		if (focused) {
			continue;
		}
		const CGFloat d = 12.0;
		CALayer *backing = [CALayer layer];
		backing.name = kSerialToolTrafficBacking;
		backing.bounds = CGRectMake(0, 0, d, d);
		backing.position = CGPointMake(CGRectGetMidX(layer.bounds), CGRectGetMidY(layer.bounds));
		backing.cornerRadius = d / 2.0;
		backing.masksToBounds = YES;
		backing.backgroundColor = [NSColor colorWithWhite:0.827 alpha:1.0].CGColor;
		backing.borderColor = [NSColor colorWithWhite:0.725 alpha:1.0].CGColor;
		backing.borderWidth = 0.5;
		backing.zPosition = -1;
		[layer insertSublayer:backing atIndex:0];
	}
}
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

func setDockVisible(visible bool) {
	if visible {
		C.serialToolSetDockVisible(1)
	} else {
		C.serialToolSetDockVisible(0)
	}
}

func syncTrafficLights(win *application.WebviewWindow, focused bool) {
	p := win.NativeWindow()
	if p == nil {
		return
	}
	if focused {
		C.serialToolSetTrafficLightsFocused(p, 1)
	} else {
		C.serialToolSetTrafficLightsFocused(p, 0)
	}
}
