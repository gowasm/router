package router

import (
	"github.com/go-humble/detect"
	"github.com/gopherjs/gopherjs/js"
	"honnef.co/go/js/dom"
	"log"
	"regexp"
	"strings"
)

var (
	// browserSupportsPushState will be true if the current browser
	// supports history.pushState and the onpopstate event.
	browserSupportsPushState bool
	document                 dom.HTMLDocument
)

func init() {
	if detect.IsClient() {
		var ok bool
		document, ok = dom.GetWindow().Document().(dom.HTMLDocument)
		if !ok {
			panic("Could not convert document to dom.HTMLDocument")
		}
		browserSupportsPushState = (js.Global.Get("onpopstate") != js.Undefined) &&
			(js.Global.Get("history") != js.Undefined) &&
			(js.Global.Get("history").Get("pushState") != js.Undefined)
	}
}

// Router is responsible for handling routes. If history.pushState is
// supported, it uses that to navigate from page to page and will listen
// to the "onpopstate" event. Otherwise, it sets the hash component of the
// url and listens to changes via the "onhashchange" event.
type Router struct {
	routes []*route
	// ShouldInterceptLinks tells the router whether or not to intercept click events
	// on links and call the Navigate method instead of the default behavior.
	// If it is set to true, the router will automatically intercept links when
	// Start, Navigate, or Back are called, or when the onpopstate event is triggered.
	ShouldInterceptLinks bool
	// listener is the js.Object representation of a listener callback.
	// It is required in order to use the RemoveEventListener method
	listener func(*js.Object)
}

// Handler is a function which is run in response to a specific
// route. A Handler takes the url parameters as an argument.
type Handler func(params map[string]string)

// New creates and returns a new router
func New() *Router {
	return &Router{
		routes: []*route{},
	}
}

type route struct {
	regex      *regexp.Regexp // Regex pattern that matches route
	paramNames []string       // Ordered list of query parameters expected by route handler
	handler    Handler        // Handler called when route is matched
}

// HandleFunc will cause the router to call f whenever window.location.pathname
// (or window.location.hash, if history.pushState is not supported) matches path.
// path can contain any number of parameters which are denoted with curly brackets.
// So, for example, a path argument of "users/{id}" will be triggered when the user
// visits users/123 and will call the handler function with params["id"] = "123".
func (r *Router) HandleFunc(path string, handler Handler) {
	r.routes = append(r.routes, newRoute(path, handler))
}

// newRoute returns a route with the given arguments. paramNames and regex
// are calculated from the path
func newRoute(path string, handler Handler) *route {
	route := &route{
		handler: handler,
	}
	strs := strings.Split(path, "/")
	strs = removeEmptyStrings(strs)
	pattern := `^`
	for _, str := range strs {
		if str[0] == '{' && str[len(str)-1] == '}' {
			pattern += `/`
			pattern += `([\w+-]*)`
			route.paramNames = append(route.paramNames, str[1:(len(str)-1)])
		} else {
			pattern += `/`
			pattern += str
		}
	}
	pattern += `/?$`
	route.regex = regexp.MustCompile(pattern)
	return route
}

// Start causes the router to listen for changes to window.location and
// trigger the appropriate handler whenever there is a change.
func (r *Router) Start() {
	if browserSupportsPushState {
		r.watchHistory()
	} else {
		r.setInitialHash()
		r.watchHash()
	}
	if r.ShouldInterceptLinks {
		r.InterceptLinks()
	}
}

// Stop causes the router to stop listening for changes, and therefore
// the router will not trigger any more router.Handler functions.
func (r *Router) Stop() {
	if browserSupportsPushState {
		js.Global.Set("onpopstate", nil)
	} else {
		js.Global.Set("onhashchange", nil)
	}
}

// Navigate will trigger the handler associated with the given path
// and update window.location accordingly. If the browser supports
// history.pushState, that will be used. Otherwise, Navigate will
// set the hash component of window.location to the given path.
func (r *Router) Navigate(path string) {
	if browserSupportsPushState {
		pushState(path)
		r.pathChanged(path)
	} else {
		setHash(path)
	}
	if r.ShouldInterceptLinks {
		r.InterceptLinks()
	}
}

// Back will cause the browser to go back to the previous page.
// It has the same effect as the user pressing the back button,
// and is just a wrapper around history.back()
func (r *Router) Back() {
	js.Global.Get("history").Call("back")
	if r.ShouldInterceptLinks {
		r.InterceptLinks()
	}
}

// InterceptLinks intercepts click events on links of the form <a href="/foo"></a>
// and calls router.Navigate("/foo") instead, which triggers the appropriate Handler
// instead of requesting a new page from the server. Since InterceptLinks works by
// setting event listeners in the DOM, you must call this function whenever the DOM
// is changed. Alternatively, you can set r.ShouldInterceptLinks to true, which will
// trigger this function whenever Start, Navigate, or Back are called, or when the
// onpopstate event is triggered. Even with r.ShouldInterceptLinks set to true, you
// may still need to call this function if you change the DOM manually without
// triggering a route.
func (r *Router) InterceptLinks() {
	for _, link := range document.Links() {
		href := link.GetAttribute("href")
		switch {
		case href == "":
			return
		case strings.HasPrefix(href, "http://"), strings.HasPrefix(href, "https://"), strings.HasPrefix(href, "//"):
			// These are external links and should behave normally.
			return
		case strings.HasPrefix(href, "#"):
			// These are anchor links and should behave normally.
			// Recall that even when we are using the hash trick, href
			// attributes should be relative paths without the "#" and
			// router will handle them appropriately.
			return
		case strings.HasPrefix(href, "/"):
			// These are relative links. The kind that we want to intercept.
			if r.listener != nil {
				// Remove the old listener (if any)
				link.RemoveEventListener("click", true, r.listener)
			}
			r.listener = link.AddEventListener("click", true, r.interceptLink)
		}
	}
}

// interceptLink is intended to be used as a callback function. It stops
// the default behavior of event and instead calls r.Navigate, passing through
// the link's href property.
func (r *Router) interceptLink(event dom.Event) {
	path := event.CurrentTarget().GetAttribute("href")
	// Only intercept the click event if we have a route which matches
	// Otherwise, just do the default.
	if bestRoute, _ := r.findBestRoute(path); bestRoute != nil {
		event.PreventDefault()
		go r.Navigate(path)
	}
}

// setInitialHash will set hash to / if there is currently no hash.
// Then it will trigger the appropriate
func (r *Router) setInitialHash() {
	if getHash() == "" {
		setHash("/")
	} else {
		r.pathChanged(getPathFromHash(getHash()))
	}
}

// pathChanged should be called whenever the path changes and will trigger
// the appropriate handler
func (r *Router) pathChanged(path string) {
	bestRoute, tokens := r.findBestRoute(path)
	// If no routes match, we throw console error and no handlers are called
	if bestRoute == nil {
		log.Fatal("Could not find route to match: " + path)
		return
	}
	// Make the params map and pass it to the handler
	params := map[string]string{}
	for i, token := range tokens {
		params[bestRoute.paramNames[i]] = token
	}
	bestRoute.handler(params)
}

// Compare given path against regex patterns of routes. Preference given to routes
// with most literal (non-query) matches. For example if we have the following:
//   Route 1: /todos/work
//   Route 2: /todos/{category}
// And the path argument is "/todos/work", the bestRoute would be todos/work
// because the string "work" matches the literal in Route 1.
func (r Router) findBestRoute(path string) (bestRoute *route, tokens []string) {
	leastParams := -1
	for _, route := range r.routes {
		matches := route.regex.FindStringSubmatch(path)
		if matches != nil {
			if (leastParams == -1) || (len(matches) < leastParams) {
				leastParams = len(matches)
				bestRoute = route
				tokens = matches[1:]
			}
		}
	}
	return bestRoute, tokens
}

// removeEmptyStrings removes any empty strings from strings
func removeEmptyStrings(strings []string) []string {
	result := []string{}
	for _, s := range strings {
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}

// watchHash listens to the onhashchange event and calls r.pathChanged when
// it changes
func (r *Router) watchHash() {
	js.Global.Set("onhashchange", func() {
		go func() {
			path := getPathFromHash(getHash())
			r.pathChanged(path)
		}()
	})
}

// watchHistory listens to the onpopstate event and calls r.pathChanged when
// it changes
func (r *Router) watchHistory() {
	js.Global.Set("onpopstate", func() {
		go func() {
			r.pathChanged(getPath())
			if r.ShouldInterceptLinks {
				r.InterceptLinks()
			}
		}()
	})
}

// getPathFromHash returns everything after the "#" character in hash.
func getPathFromHash(hash string) string {
	return strings.SplitN(hash, "#", 2)[1]
}

// getHash is an alias for js.Global.Get("location").Get("hash").String()
func getHash() string {
	return js.Global.Get("location").Get("hash").String()
}

// setHash is an alias for js.Global.Get("location").Set("hash", hash)
func setHash(hash string) {
	js.Global.Get("location").Set("hash", hash)
}

// getPath is an alias for js.Global.Get("location").Get("pathname").String()
func getPath() string {
	return js.Global.Get("location").Get("pathname").String()
}

// pushState is an alias for js.Global.Get("history").Call("pushState", nil, "", path)
func pushState(path string) {
	js.Global.Get("history").Call("pushState", nil, "", path)
}
