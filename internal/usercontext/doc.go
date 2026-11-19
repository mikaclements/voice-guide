// Package usercontext loads what an agent may know about the user it talks to
// from earlier conversations, and saves what a session learned. It keeps its
// own data: other modules call its service, never its store, and it imports
// none of the other modules.
package usercontext
