package main

import "strings"

// flagsFirst moves positional arguments behind the flags, so `termit-mn restore archive.tar.gz --config x`
// parses the way it was typed. Go's flag package stops at the first positional argument, and silently
// treating "--config x" as two more positionals is a bug the operator cannot see.
//
// boolean names the flags that take no value. A flag written as --name=value is left alone.
func flagsFirst(args []string, boolean map[string]bool) []string {
	flags := make([]string, 0, len(args))
	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		isFlag := len(arg) > 1 && arg[0] == '-'
		if !isFlag {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		if strings.Contains(arg, "=") || boolean[arg] {
			continue
		}
		// A value belongs to the flag; a negative number would start with "-" too, and none of our
		// flags take one.
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, positional...)
}
