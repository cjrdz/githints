package lang

import (
	"reflect"
	"strconv"
	"testing"
)

// symbolLines returns "kind name start-end" for each symbol, for comparing a
// whole file's output at once.
func symbolLines(symbols []Symbol) []string {
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, string(s.Kind)+" "+s.Name+" "+strconv.Itoa(s.LineStart)+"-"+strconv.Itoa(s.LineEnd))
	}
	return out
}

// TestCSpec: declarations in each common style, and none of the statements
// that share their shape (return f(x);, a macro call, an include guard).
func TestCSpec(t *testing.T) {
	src := "" +
		"#include \"app.h\"\n" + // 1
		"#define APP_H_\n" +
		"#define MAX_USERS 64\n" +
		"#define MIN(a, b) ((a) < (b) ? (a) : (b))\n" +
		"typedef int (*cmp_fn)(const void *, const void *);\n" + // 5
		"typedef unsigned long u64;\n" +
		"typedef struct {\n" +
		"    int x, y;\n" +
		"} point_t;\n" +
		"struct user {\n" + // 10
		"    int (*greet)(struct user *u);\n" +
		"};\n" +
		"static int helper(int a);\n" +
		"static DEFINE_MUTEX(lock);\n" +
		"MODULE_LICENSE(\"GPL\");\n" + // 15
		"static const char *\n" +
		"user_name (const struct user *u)\n" +
		"{\n" +
		"    return lookup(u->name);\n" +
		"}\n" + // 20
		"int main(int argc, char **argv)\n" +
		"{\n" +
		"    if (argc > 1) {\n" +
		"        return run(argv[1]);\n" +
		"    }\n" + // 25
		"    return 0;\n" +
		"}\n" +
		"static int __init mod_init(void) {\n" +
		"    return register_thing(x);\n" +
		"}\n" // 30
	symbols, _ := parseWith(t, "c", "src/app.c", src)
	want := []string{
		"const MAX_USERS 3-3", "func MIN 4-4", "type cmp_fn 5-5", "type u64 6-6", "type point_t 9-9",
		"type user 10-12", "func helper 13-13", "func user_name 17-20", "func main 21-27", "func mod_init 28-30",
	}
	if got := symbolLines(symbols); !reflect.DeepEqual(got, want) {
		t.Errorf("symbols:\n got %q\nwant %q", got, want)
	}
}

// TestCppSpec: classes past export macros and templates, members declared in
// a class and defined outside it (named with the class), and an Allman
// constructor with an initializer list whose body is not read as
// declarations.
func TestCppSpec(t *testing.T) {
	src := "" +
		"namespace ui {\n" + // 1
		"class MYLIB_API Widget : public QWidget {\n" +
		"    Q_OBJECT\n" +
		"public:\n" +
		"    explicit Widget(QWidget *parent = nullptr);\n" + // 5
		"    virtual void draw() const override;\n" +
		"    Q_INVOKABLE int count() const;\n" +
		"    bool operator==(const Widget &o) const;\n" +
		"};\n" +
		"template <typename T>\n" + // 10
		"class Box {\n" +
		"    T value;\n" +
		"};\n" +
		"enum class Color : uint8_t { Red, Green };\n" +
		"using Ptr = std::unique_ptr<Widget>;\n" + // 15
		"struct Fwd;\n" +
		"Widget::Widget(QWidget *parent)\n" +
		"    : QWidget(parent),\n" +
		"      size_(0)\n" +
		"{\n" + // 20
		"    connect(this, &Widget::changed, this, &Widget::update);\n" +
		"    std::string s = format(x);\n" +
		"}\n" +
		"const std::string &Widget::name() const\n" +
		"{\n" + // 25
		"    return name_;\n" +
		"}\n" +
		"auto Widget::size() const -> int { return size_; }\n" +
		"static auto handler = [](int x) {\n" +
		"    return compute(x);\n" + // 30
		"};\n" +
		"template <typename T>\n" +
		"T clamp(T v, T lo, T hi) {\n" +
		"    return v;\n" +
		"}\n" + // 35
		"}\n"
	symbols, _ := parseWith(t, "cpp", "src/widget.cpp", src)
	want := []string{
		"type Widget 2-9", "func Widget 5-5", "func draw 6-6", "func count 7-7", "func operator== 8-8",
		"type Box 11-13", "type Color 14-14", "type Ptr 15-15",
		"method Widget::Widget 17-23", "method Widget::name 24-27", "method Widget::size 28-28", "func clamp 33-35",
	}
	if got := symbolLines(symbols); !reflect.DeepEqual(got, want) {
		t.Errorf("symbols:\n got %q\nwant %q", got, want)
	}
}

// TestAllmanBodyIsNotDeclarations: a brace on the line below a header opens
// its body, for every brace language: the declaration ends where the body
// does, and a local prototype in the body is not a function of the file. C#
// is written this way by default.
func TestAllmanBodyIsNotDeclarations(t *testing.T) {
	src := "public class Svc\n{\n    public void Run()\n    {\n        Go();\n    }\n}\n"
	symbols, _ := parseWith(t, "csharp", "Svc.cs", src)
	if got, want := symbolLines(symbols), []string{"type Svc 1-7", "method Run 3-6"}; !reflect.DeepEqual(got, want) {
		t.Errorf("csharp:\n got %q\nwant %q", got, want)
	}
	src = "int main(void)\n{\n    int local(int);\n    return 0;\n}\n"
	symbols, _ = parseWith(t, "c", "main.c", src)
	if got, want := symbolLines(symbols), []string{"func main 1-5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("c:\n got %q\nwant %q", got, want)
	}
}

// TestResolveInclude: a quoted include beside the file, a project header
// through an include/ directory above it, a parent-relative path, and system
// headers kept as written.
func TestResolveInclude(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/net/util.h":                "",
		"src/common.h":                  "",
		"include/mylib/api.h":           "",
		"libs/core/include/core/core.h": "",
		"libs/core/src/detail.hpp":      "",
		"src/stdio.h":                   "", // shadows only a quoted include
	})
	defer beginSpecScan(root)()
	for _, c := range []struct {
		file, name string
		quoted     bool
		want       string
	}{
		{"src/net/http.c", "util.h", true, "src/net/util.h"},
		{"src/net/http.c", "mylib/api.h", false, "include/mylib/api.h"},
		{"src/net/http.c", "../common.h", true, "src/common.h"},
		{"src/net/http.c", "common.h", true, "src/common.h"},
		{"libs/core/src/core.cpp", "core/core.h", false, "libs/core/include/core/core.h"},
		{"libs/core/src/core.cpp", "detail.hpp", true, "libs/core/src/detail.hpp"},
		{"src/net/http.c", "vector", false, "vector"},
		{"src/net/http.c", "missing.h", true, "missing.h"},
		{"src/net/http.c", "../../../etc/passwd", true, "../../../etc/passwd"},
		{"src/net/http.c", "/usr/include/stdio.h", false, "/usr/include/stdio.h"},
	} {
		if got := resolveInclude(root, c.file, c.name, c.quoted); got != c.want {
			t.Errorf("%s includes %q = %q, want %q", c.file, c.name, got, c.want)
		}
	}
	// Outside a scan the include keeps its spelling.
	if got := resolveInclude("", "src/net/http.c", "util.h", true); got != "util.h" {
		t.Errorf("no root = %q", got)
	}
}

// TestIncludeImportPathRoundTrip: the key a header is imported by is what an
// include of it resolves to, so the two meet in the graph.
func TestIncludeImportPathRoundTrip(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"include/app/api.h": "",
		"src/main.c":        "#include <app/api.h>\n#include \"api_impl.h\"\n",
		"src/api_impl.h":    "",
	})
	reg := NewRegistry()
	defer BeginScans(reg.AllParsers(), root)()
	_, imports, err := reg.ForPath("src/main.c").Parse("src/main.c", []byte("#include <app/api.h>\n#include \"api_impl.h\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, f := range []string{"include/app/api.h", "src/api_impl.h"} {
		k, err := reg.ImportPath(root, f)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if got := importPaths(imports); !reflect.DeepEqual(got, keys) {
		t.Errorf("imports %q, keys %q", got, keys)
	}
}

// TestCMakeSpec: targets, options and functions, add_subdirectory as a link
// to the directory, and a bracket comment that hides one.
func TestCMakeSpec(t *testing.T) {
	src := "option(APP_TESTS \"tests\" ON)\nadd_subdirectory(src)\n#[[ add_subdirectory(old)\n]]\n# add_subdirectory(gone)\nadd_executable(app main.cpp)\nfunction(app_setup target)\nendfunction()\nqt_add_executable(todo\n  src/main.cpp)\n"
	symbols, imports := parseWith(t, "cmake", "CMakeLists.txt", src)
	if got, want := symbolLines(symbols), []string{"var APP_TESTS 1-1", "type app 6-6", "func app_setup 7-7", "type todo 9-10"}; !reflect.DeepEqual(got, want) {
		t.Errorf("symbols %q, want %q", got, want)
	}
	if got, want := importPaths(imports), []string{"src"}; !reflect.DeepEqual(got, want) {
		t.Errorf("imports %q, want %q", got, want)
	}
}

// TestBodylessDeclarationDoesNotReachTheNext: a declaration with no braces,
// followed by a blank line and then another declaration, ends at its own
// header. Looking past the blank line for a brace once gave a Kotlin
// expression-bodied function the next function's body.
func TestBodylessDeclarationDoesNotReachTheNext(t *testing.T) {
	src := "class C(private val r: R) {\n\n    fun list(): List<O> = r.all()\n\n    @Post(\"/x\")\n    fun cancel(id: Long) {}\n}\n\ndata class Note(\n    val id: Long,\n)\n\n@Dao\ninterface NoteDao {\n}\n"
	symbols, _ := parseWith(t, "kotlin", "a/C.kt", src)
	want := []string{"type C 1-7", "func list 3-3", "func cancel 6-6", "type Note 9-11", "type NoteDao 14-15"}
	if got := symbolLines(symbols); !reflect.DeepEqual(got, want) {
		t.Errorf("symbols:\n got %q\nwant %q", got, want)
	}
}
