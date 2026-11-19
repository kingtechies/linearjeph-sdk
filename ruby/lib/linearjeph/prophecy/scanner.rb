# frozen_string_literal: true

require "json"
require "time"

module Linearjeph
  module Prophecy
    # Local codebase scanner. Reads files on this machine and returns a manifest Hash (schema 1). It never includes source
    # code, env values or secrets: env NAMES only, secrets_suspected is a count. Skips node_modules, .git, vendor, venv,
    # dist, build; files over 1 MB; more than 20,000 files; symlinks (nothing outside the root is read).
    class Scanner
      MAX_FILES = 20_000
      MAX_BYTES = 1_048_576
      MAX_DEPS = 2000
      MAX_ROUTES = 3000
      MAX_FEATURES = 150
      MAX_ENV = 500

      SKIP_DIRS = %w[node_modules .git vendor venv .venv dist build __pycache__ .next .nuxt .cache coverage .tox target
                     .gradle .idea .terraform bower_components].freeze
      CODE_EXTS = %w[go py js mjs cjs ts tsx jsx php rb java kt kts swift dart rs c h cc cpp cs vue svelte scala ex exs lua
                     sh sql html css scss].freeze
      TEXT_EXTS = %w[json yml yaml toml ini cfg conf env txt md mod lock xml tf properties].freeze
      INTEGRATIONS = [%w[stripe stripe], %w[paddle paddle], %w[paystack paystack], %w[flutterwave flutterwave],
                      %w[bachs bachs], %w[auth0 auth0], %w[clerk clerk], %w[supabase supabase], %w[firebase firebase],
                      %w[sentry sentry], %w[segment segment], %w[mixpanel mixpanel], %w[posthog posthog],
                      %w[twilio twilio], %w[sendgrid sendgrid], %w[resend resend], %w[openai openai],
                      %w[anthropic anthropic], %w[aws-sdk s3], %w[boto3 s3], %w[aws_s3 s3], %w[s3_bucket s3],
                      %w[mailgun mailgun], %w[postmark postmark], %w[algolia algolia], %w[cloudinary cloudinary],
                      %w[plaid plaid], %w[shopify shopify], %w[datadog datadog], %w[newrelic newrelic],
                      %w[linearjeph linearjeph], %w[redis redis], %w[kafka kafka], %w[rabbitmq rabbitmq]].freeze
      FRAMEWORK_DEPS = {
        "npm:express" => "express", "npm:next" => "next", "npm:react" => "react", "npm:vue" => "vue", "npm:nuxt" => "nuxt",
        "npm:svelte" => "svelte", "npm:@nestjs/core" => "nestjs", "npm:fastify" => "fastify", "npm:koa" => "koa",
        "npm:hono" => "hono", "npm:@angular/core" => "angular", "pypi:django" => "django", "pypi:flask" => "flask",
        "pypi:fastapi" => "fastapi", "pypi:tornado" => "tornado", "pypi:starlette" => "starlette",
        "composer:laravel/framework" => "laravel", "composer:symfony/framework-bundle" => "symfony",
        "composer:slim/slim" => "slim", "gem:rails" => "rails", "gem:sinatra" => "sinatra", "gem:hanami" => "hanami",
        "gem:roda" => "roda"
      }.freeze
      GO_FRAMEWORKS = { "github.com/gin-gonic/gin" => "gin", "github.com/go-chi/chi" => "chi",
                        "github.com/labstack/echo" => "echo", "github.com/gofiber/fiber" => "fiber",
                        "github.com/gorilla/mux" => "gorilla-mux" }.freeze
      TEST_DEPS = { "jest" => "jest", "vitest" => "vitest", "mocha" => "mocha", "@playwright/test" => "playwright",
                    "cypress" => "cypress", "pytest" => "pytest", "phpunit/phpunit" => "phpunit", "pestphp/pest" => "pest",
                    "rspec" => "rspec", "rspec-rails" => "rspec", "minitest" => "minitest" }.freeze
      SECRETS = [/AKIA[0-9A-Z]{16}/, /-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----/, /sk_live_[0-9A-Za-z]{16,}/,
                 /gh[pousr]_[A-Za-z0-9]{30,}/, /xox[baprs]-[A-Za-z0-9-]{10,}/, /ljp_sk_[A-Za-z0-9_-]{20,}/,
                 /AIza[0-9A-Za-z_-]{35}/].freeze
      SKIP_FEATURE_DIRS = %w[src lib test tests spec docs scripts public static assets utils common types config migrations
                             database bin tmp examples e2e __tests__ node_modules app].freeze
      CONTAINER_DIRS = %w[src app apps packages internal pkg modules lib cmd services].freeze

      RE_GO_ROUTE = /\b(\w+)\.(Get|Post|Put|Patch|Delete|Head|Options|GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Any|Handle|HandleFunc)\(\s*(?:"([^"]+)"|`([^`]+)`)/.freeze
      RE_GO_GROUP = /(\w+)\s*:?=\s*(\w+)\.(?:Group|Route)\(\s*"([^"]*)"/.freeze
      RE_JS_ROUTE = /\b(?:app|router|server|fastify|api|routes)\.(get|post|put|patch|delete|all)\(\s*['"`]([^'"`]+)['"`]/.freeze
      RE_PHP_ROUTE = /Route::(get|post|put|patch|delete|any|resource|apiResource)\(\s*['"]([^'"]+)['"]/.freeze
      RE_RB_ROUTE = /^\s*(get|post|put|patch|delete)\s+['"]([^'"]+)['"]/.freeze
      RE_RB_RES = /^\s*(resources?)\s+:(\w+)/.freeze
      RE_RB_NS = /^\s*(?:namespace|scope)\s+:?['"]?(\w+)['"]?.*\bdo\b/.freeze
      RE_RB_DO = /\bdo\s*(\|[^|]*\|)?\s*$/.freeze
      RE_PY_DJANGO = /\b(?:re_)?path\(\s*r?['"]([^'"]*)['"]/.freeze
      RE_PY_DECO = /@\w+\.(route|get|post|put|patch|delete)\(\s*['"]([^'"]+)['"]/.freeze
      RE_FLAG = /(?:isEnabled|is_enabled|isFeatureEnabled|feature_enabled\??|featureEnabled|flagEnabled|Feature::active|useFlag|useFeatureFlag|getBooleanFlag|feature_flag|FeatureFlag)\(\s*['"]([\w.:-]{2,60})['"]/i.freeze
      RE_ENV_FILE = /^\s*(?:export\s+)?([A-Z][A-Z0-9_]*)\s*=/.freeze
      RE_ENV_CODE = [/(?:Getenv|LookupEnv|getenv|env|fetch|get|config)\(\s*["']([A-Z][A-Z0-9_]{1,63})["']/,
                     /(?:process\.env|import\.meta\.env)\.([A-Z][A-Z0-9_]{1,63})/,
                     /(?:ENV|environ|_ENV|_SERVER)\[\s*["']([A-Z][A-Z0-9_]{1,63})["']/].freeze
      RE_PLAN = /\b(?:name|plan|id|slug|key|tier)\b["']?\s*(?:=>|[:=])\s*["']([A-Za-z][\w .-]{0,39})["']/i.freeze
      RE_PRICE = /\b(?:price|amount|monthly|mrr|cost)\w*["']?\s*(?:=>|[:=])\s*(\d{1,6}(?:\.\d{1,2})?)\b/i.freeze
      RE_CUR = /\b(USD|EUR|GBP|NGN|GHS|KES|ZAR|CAD|AUD|INR)\b/i.freeze
      RE_PY_REQ = /\A([A-Za-z0-9_.\-]+)(?:\[[^\]]*\])?\s*(?:(==|>=|<=|~=|!=|>|<)\s*([\w.*+-]+))?/.freeze

      def scan(root)
        abs = File.realpath(root)
        raise Error, "scan: not a directory: #{root}" unless File.directory?(abs)

        @files = 0
        @loc = 0
        @langs = Hash.new(0)
        @deps = {}
        @routes = []
        @route_seen = {}
        @env = {}
        @dir_files = Hash.new(0)
        @flag_seen = {}
        @flags = []
        @ci = {}
        @hosting = {}
        @test_fw = {}
        @price_seen = {}
        @pricing = []
        @frameworks = []
        @infra = { "docker" => false, "compose" => false, "kubernetes" => false, "terraform" => false, "serverless" => false }
        @security = { "lockfile" => false, "env_committed" => false, "secrets_suspected" => 0 }
        @test_files = 0
        @description = ""
        walk(abs, "")
        finish(abs)
      end

      private

      def walk(abs, rel_dir)
        dir = rel_dir.empty? ? abs : File.join(abs, rel_dir)
        names = begin
          Dir.children(dir).sort
        rescue SystemCallError
          return
        end
        names.each do |name|
          return if @files >= MAX_FILES

          full = File.join(dir, name)
          rel = rel_dir.empty? ? name : "#{rel_dir}/#{name}"
          next if File.symlink?(full) # never follow symlinks: no escape from the root

          if File.directory?(full)
            next if SKIP_DIRS.include?(name)

            @infra["kubernetes"] = true if %w[k8s kubernetes helm charts].include?(name)
            @ci["github-actions"] = true if rel == ".github/workflows"
            @ci["circleci"] = true if rel == ".circleci"
            walk(abs, rel)
          elsif File.file?(full)
            size = begin
              File.size(full)
            rescue SystemCallError
              next
            end
            next if size > MAX_BYTES

            @files += 1
            process_file(full, rel, name)
          end
        end
      end

      def add_dep(eco, name, ver)
        k = "#{eco}:#{name}"
        return if name.empty? || @deps.key?(k) || @deps.size >= MAX_DEPS

        @deps[k] = { "ecosystem" => eco, "name" => name, "version" => ver.to_s }
      end

      def add_route(method, path, file)
        return if path.empty? || @routes.size >= MAX_ROUTES

        method = method.upcase
        path = path.gsub(/\{(\w+)[^}]*\}/, ':\1')
        k = "#{method} #{path} #{file}"
        return if @route_seen[k]

        @route_seen[k] = true
        @routes << { "method" => method, "path" => path, "file" => file }
      end

      def add_env(n)
        @env[n] = true if @env.size < MAX_ENV
      end

      def file_infra(rel, base)
        lb = base.downcase
        if lb == "dockerfile" || lb.start_with?("dockerfile.") || lb.end_with?(".dockerfile")
          @infra["docker"] = true
        elsif %w[docker-compose.yml docker-compose.yaml compose.yml compose.yaml].include?(lb) || lb.start_with?("docker-compose.")
          @infra["compose"] = true
        elsif lb.end_with?(".tf")
          @infra["terraform"] = true
        elsif %w[serverless.yml serverless.yaml serverless.ts serverless.js].include?(lb)
          @infra["serverless"] = true
        end
        ci = { ".gitlab-ci.yml" => "gitlab", "jenkinsfile" => "jenkins", "azure-pipelines.yml" => "azure-pipelines",
               "bitbucket-pipelines.yml" => "bitbucket", ".travis.yml" => "travis" }
        @ci[ci[lb]] = true if ci.key?(lb)
        host = { "vercel.json" => "vercel", "netlify.toml" => "netlify", "fly.toml" => "fly", "render.yaml" => "render",
                 "render.yml" => "render", "procfile" => "heroku", "railway.json" => "railway", "railway.toml" => "railway",
                 "wrangler.toml" => "cloudflare", "wrangler.json" => "cloudflare", "firebase.json" => "firebase" }
        @hosting[host[lb]] = true if host.key?(lb)
        @hosting["gcp-app-engine"] = true if lb == "app.yaml" && !rel.include?("/")
        @infra["kubernetes"] = true if rel.include?("k8s/") || rel.include?("kubernetes/")
      end

      def test_file?(base)
        lb = base.downcase
        %w[_test.go _test.py _spec.rb _test.rb test.php _test.exs].any? { |s| lb.end_with?(s) } ||
          (lb.start_with?("test_") && lb.end_with?(".py")) || lb.include?(".test.") || lb.include?(".spec.")
      end

      def process_file(full, rel, base)
        file_infra(rel, base)
        ext = File.extname(base).delete_prefix(".").downcase
        lb = base.downcase
        env_file = lb.start_with?(".env") || lb.end_with?(".env")
        @security["env_committed"] = true if env_file && lb !~ /example|sample|template|dist/
        case lb
        when "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "composer.lock", "go.sum", "poetry.lock", "pipfile.lock",
             "uv.lock", "bun.lockb", "gemfile.lock"
          @security["lockfile"] = true
        when "artisan" then @frameworks << "laravel"
        when "manage.py" then @frameworks << "django"
        when "conftest.py" then @test_fw["pytest"] = true
        when "phpunit.xml", "phpunit.xml.dist" then @test_fw["phpunit"] = true
        end
        @frameworks << "rails" if rel == "config/routes.rb"
        code = CODE_EXTS.include?(ext)
        return if !code && !TEXT_EXTS.include?(ext) && !env_file && lb != "gemfile" && !lb.start_with?("readme")

        data = begin
          File.binread(full)
        rescue SystemCallError
          return
        end
        return if data.byteslice(0, 512).include?("\0")

        text = data.dup.force_encoding("UTF-8").scrub("")
        scan_code(full, rel, base, ext, lb, text) if code
        text.each_line { |ln| (m = RE_ENV_FILE.match(ln)) && add_env(m[1]) } if env_file
        manifest_file(lb, text)
        @description = describe(text) if lb.start_with?("readme") && !rel.include?("/") && @description.empty?
        SECRETS.each { |re| @security["secrets_suspected"] += text.scan(re).size }
        return unless rel =~ /plan|pricing|price|billing|subscription|tier/i && @pricing.size < 20 &&
                      %w[json js ts py php rb go yml yaml].include?(ext)

        pricing_hints(text)
      end

      def scan_code(_full, rel, base, ext, lb, text)
        @langs[ext] += 1
        @loc += text.each_line.count { |l| !l.strip.empty? }
        if test_file?(base)
          @test_files += 1
          if lb.end_with?("_test.go") then @test_fw["go test"] = true
          elsif ext == "py"
            @test_fw["pytest"] = true if text.include?("pytest")
            @test_fw["unittest"] = true if text.include?("unittest")
          elsif ext == "rb" then @test_fw[lb.end_with?("_spec.rb") ? "rspec" : "minitest"] = true
          elsif ext == "php" then @test_fw["phpunit"] = true
          end
        end
        if (i = rel.index("/")) && i.positive?
          top = rel[0, i]
          if CONTAINER_DIRS.include?(top)
            parts = rel.split("/", 3)
            @dir_files["#{top}/#{parts[1]}"] += 1 if parts.size == 3
          else
            @dir_files[top] += 1
          end
        end
        routes_for(rel, ext, text)
        RE_ENV_CODE.each { |re| text.scan(re) { |m| add_env(m[0]) } }
        text.scan(RE_FLAG) do |m|
          n = m[0].downcase
          next if @flag_seen[n] || @flags.size >= 100

          @flag_seen[n] = true
          @flags << { "name" => n, "source" => "flag", "paths" => [rel] }
        end
      end

      def pricing_hints(text)
        plan = ""
        cur = ""
        text.each_line do |ln|
          next if ln.size > 2000

          (m = RE_CUR.match(ln)) && (cur = m[1].upcase)
          plans = ln.enum_for(:scan, RE_PLAN).map { Regexp.last_match }
          prices = ln.enum_for(:scan, RE_PRICE).map { Regexp.last_match }
          pi = 0
          prices.each do |pr|
            while pi < plans.size && plans[pi].begin(0) < pr.begin(0)
              plan = plans[pi][1].strip
              pi += 1
            end
            num = pr[1]
            amt = num.to_f
            next if amt <= 0

            name = plan.empty? ? "default" : plan
            k = "#{name}|#{num}"
            next if @price_seen[k] || @pricing.size >= 20

            @price_seen[k] = true
            @pricing << { "plan" => name, "amount" => amt, "currency" => cur.empty? ? "USD" : cur }
          end
          while pi < plans.size
            plan = plans[pi][1].strip
            pi += 1
          end
        end
      end

      def describe(text)
        t = text.byteslice(0, 2048).to_s.scrub("")
        t = t.gsub(/!\[[^\]]*\]\([^)]*\)/, "").gsub(/\[([^\]]*)\]\([^)]*\)/, '\1').gsub(/<[^>]+>/, "")
        t = t.gsub("```", " ").delete("`*").tr("#", " ")
        t.split.join(" ")
      end

      def routes_for(rel, ext, text)
        case ext
        when "go" then go_routes(rel, text)
        when "php"
          text.scan(RE_PHP_ROUTE) do |verb, path|
            p = "/#{path.sub(%r{\A/+}, '')}"
            if %w[resource apiResource].include?(verb)
              add_route("GET", p, rel)
              add_route("POST", p, rel)
              add_route("GET", "#{p}/:id", rel)
              add_route("PUT", "#{p}/:id", rel)
              add_route("DELETE", "#{p}/:id", rel)
            else
              add_route(verb, p, rel)
            end
          end
        when "rb" then ruby_routes(rel, text)
        when "py"
          if rel.end_with?("urls.py")
            text.scan(RE_PY_DJANGO) { |(p)| add_route("ANY", "/#{p.gsub(/<(?:\w+:)?(\w+)>/, ':\1').sub(%r{\A/+}, '')}", rel) }
          end
          text.scan(RE_PY_DECO) { |verb, p| add_route(verb == "route" ? "ANY" : verb, p, rel) if p.start_with?("/") }
        when "js", "mjs", "cjs", "ts", "jsx", "tsx"
          text.scan(RE_JS_ROUTE) { |verb, p| add_route(verb == "all" ? "ANY" : verb, p, rel) if p.start_with?("/") || p == "*" }
          next_route(rel, ext)
        end
      end

      def go_routes(rel, text)
        prefix = {}
        text.scan(RE_GO_GROUP) { |v, recv, p| prefix[v] = "#{prefix[recv]}#{p}" }
        text.scan(RE_GO_ROUTE) do |recv, verb, p1, p2|
          p = p1 || p2
          method = verb.upcase
          if %w[HANDLE HANDLEFUNC].include?(method)
            method = "ANY"
            f = p.strip.split(/\s+/)
            method, p = f if f.size == 2
            i = p.index("/")
            p = p[i..] if i&.positive?
          end
          next unless p.start_with?("/")

          add_route(method, "#{prefix[recv]}#{p}", rel)
        end
      end

      def ruby_routes(rel, text)
        unless rel.end_with?("routes.rb") || rel.include?("routes/")
          if text.include?("Sinatra") || text.include?("Roda")
            text.each_line { |ln| (m = RE_RB_ROUTE.match(ln)) && add_route(m[1], m[2], rel) }
          end
          return
        end
        stack = []
        text.each_line do |ln|
          ln = ln.chomp
          if ln =~ /^\s*end\b/
            stack.pop
            next
          end
          pre = stack.reject(&:empty?).join("/")
          pre = "/#{pre}" unless pre.empty?
          if (m = RE_RB_NS.match(ln))
            stack << m[1]
            next
          end
          if (m = RE_RB_RES.match(ln))
            p = "#{pre}/#{m[2]}"
            if m[1] == "resources"
              [["GET", p], ["POST", p], ["GET", "#{p}/:id"], ["PUT", "#{p}/:id"], ["DELETE", "#{p}/:id"]].each { |mt, pt| add_route(mt, pt, rel) }
            else
              add_route("GET", p, rel)
            end
          elsif (m = RE_RB_ROUTE.match(ln))
            add_route(m[1], "#{pre}/#{m[2].sub(%r{\A/+}, '')}", rel)
          end
          stack << "" if RE_RB_DO.match?(ln)
        end
      end

      def next_route(rel, ext)
        r = rel.start_with?("src/") ? rel[4..] : rel
        if r.start_with?("app/")
          kind = :app
          sub = r[4..]
        elsif r.start_with?("pages/")
          kind = :pages
          sub = r[6..]
        else
          return
        end
        segs = sub[0, sub.size - ext.size - 1].split("/")
        last = segs.pop.to_s
        conv = lambda do |x|
          if x.start_with?("(") && x.end_with?(")") then ""
          elsif x.start_with?("[") && x.end_with?("]") then ":#{x.delete('[].')}"
          else x
          end
        end
        out = segs.map { |x| conv.call(x) }.reject(&:empty?)
        method = "GET"
        if kind == :app
          if last == "route" then method = "ANY"
          elsif last != "page" then return
          end
        else
          return if last.start_with?("_")

          out << conv.call(last) if last != "index" && !conv.call(last).empty?
          method = "ANY" if out.first == "api"
        end
        add_route(method, "/#{out.join('/')}", rel)
      end

      def manifest_file(lb, text)
        case lb
        when "go.mod" then go_mod(text)
        when "package.json", "composer.json"
          pj = JSON.parse(text)
          return unless pj.is_a?(Hash)

          npm = lb == "package.json"
          (npm ? %w[dependencies devDependencies] : %w[require require-dev]).each do |k|
            m = pj[k]
            next unless m.is_a?(Hash)

            m.keys.sort.each do |n|
              next if !npm && (n == "php" || n.start_with?("ext-") || n.start_with?("lib-"))

              add_dep(npm ? "npm" : "composer", n, m[n].is_a?(String) ? m[n] : "")
            end
          end
        when "gemfile.lock" then gem_lock(text)
        when "gemfile"
          text.each_line { |ln| (m = /^\s*gem\s+['"]([^'"]+)['"](?:\s*,\s*['"]([^'"]+)['"])?/.match(ln)) && add_dep("gem", m[1], m[2].to_s) }
        when /\Arequirements.*\.txt\z/ then text.each_line { |ln| py_req(ln) }
        when "pyproject.toml" then pyproject(text)
        end
      rescue JSON::ParserError
        nil
      end

      def go_mod(text)
        block = false
        text.each_line do |ln|
          ln = ln.strip
          if ln.start_with?("require (")
            block = true
            next
          end
          if block && ln == ")"
            block = false
            next
          end
          unless block
            next unless ln.start_with?("require ")

            ln = ln[8..].strip
          end
          next if ln.include?("// indirect")

          f = ln.split(/\s+/)
          add_dep("go", f[0], f[1]) if f.size >= 2 && !f[0].start_with?("//")
        end
      end

      def gem_lock(text)
        in_gem = false
        text.each_line do |ln|
          ln = ln.chomp
          next if ln.empty?

          unless ln.start_with?(" ")
            in_gem = ln.start_with?("GEM")
            next
          end
          next unless in_gem && ln.start_with?("    ") && !ln.start_with?("     ")

          t = ln.strip
          i = t.index(" (")
          add_dep("gem", t[0, i], t[(i + 2)..-2]) if i&.positive? && t.end_with?(")")
        end
      end

      def pyproject(text)
        section = ""
        in_arr = false
        text.each_line do |ln|
          t = ln.strip
          if t.start_with?("[")
            section = t.delete("[] ")
            in_arr = false
            next
          end
          if section == "project" && t.start_with?("dependencies") && t.include?("[")
            in_arr = true
            t = t[(t.index("[") + 1)..]
          end
          if in_arr
            t.scan(/"([^"]+)"|'([^']+)'/) { |a, b| py_req(a || b) }
            in_arr = false if t.include?("]")
            next
          end
          next unless %w[tool.poetry.dependencies tool.poetry.dev-dependencies tool.poetry.group.dev.dependencies].include?(section)

          i = t.index("=")
          next unless i&.positive? && !t.start_with?("#")

          name = t[0, i].strip
          next if name == "python"

          ver = t[(i + 1)..].strip.delete("\"'")
          (vm = /version\s*=\s*["']([^"']+)/.match(t)) && (ver = vm[1])
          add_dep("pypi", name.downcase, ver)
        end
      end

      def py_req(ln)
        ln = ln.strip
        return if ln.empty? || ln.start_with?("#") || ln.start_with?("-")

        ln = ln[0, ln.index(";")] if ln.index(";")&.positive?
        m = RE_PY_REQ.match(ln) or return
        op = m[2].to_s
        ver = m[3].to_s
        ver = op + ver if !op.empty? && op != "=="
        add_dep("pypi", m[1].downcase, ver)
      end

      def route_group(path)
        path.split("/").each do |seg|
          l = seg.downcase
          next if l.empty? || l == "api" || l == "*" || l.start_with?(":") || l.start_with?("{")
          next if l =~ /\Av\d/

          return l
        end
        ""
      end

      def finish(abs)
        fw = {}
        @frameworks.each { |f| fw[f] = true }
        test_fw = @test_fw.dup
        lower = []
        @deps.each_value do |d|
          k = "#{d['ecosystem']}:#{d['name']}"
          fw[FRAMEWORK_DEPS[k]] = true if FRAMEWORK_DEPS.key?(k)
          GO_FRAMEWORKS.each { |pfx, n| fw[n] = true if d["ecosystem"] == "go" && d["name"].start_with?(pfx) }
          test_fw[TEST_DEPS[d["name"]]] = true if TEST_DEPS.key?(d["name"])
          lower << d["name"].downcase
        end
        if @routes.any? { |r| r["file"].end_with?(".go") } && !(fw.key?("gin") || fw.key?("chi") || fw.key?("echo") || fw.key?("fiber"))
          fw["net/http"] = true
        end
        env = @env.keys.sort
        hay = lower + env.map(&:downcase)
        integ = {}
        INTEGRATIONS.each { |kw, name| integ[name] = true if hay.any? { |h| h.include?(kw) } }

        groups = {}
        @routes.each do |r|
          n = route_group(r["path"])
          next if n.empty?

          groups[n] ||= []
          groups[n] << r["path"] if groups[n].size < 5
        end
        features = []
        seen = {}
        groups.each do |n, paths|
          seen[n] = true
          features << { "name" => n, "source" => "route", "paths" => paths }
        end
        @flags.each do |f|
          next if seen[f["name"]]

          seen[f["name"]] = true
          features << f
        end
        env.each do |e|
          %w[FEATURE_ FF_ ENABLE_ ENABLED_].each do |pfx|
            next unless e.start_with?(pfx)

            n = e[pfx.size..].downcase
            next if n.empty? || seen[n]

            seen[n] = true
            features << { "name" => n, "source" => "config", "paths" => [e] }
          end
        end
        @dir_files.keys.sort.each do |d|
          n = File.basename(d).downcase
          next if seen[n] || n.start_with?(".") || SKIP_FEATURE_DIRS.include?(n)

          seen[n] = true
          features << { "name" => n, "source" => "dir", "paths" => [d] }
        end
        infra = @infra.merge("ci" => @ci.keys.sort, "hosting_hints" => @hosting.keys.sort)
        {
          "schema" => 1,
          "scanned_at" => Time.now.utc.strftime("%Y-%m-%dT%H:%M:%SZ"),
          "root_name" => File.basename(abs),
          "languages" => @langs.sort.to_h,
          "loc" => @loc,
          "frameworks" => fw.keys.sort,
          "dependencies" => @deps.values,
          "routes" => @routes,
          "features" => features.first(MAX_FEATURES),
          "integrations" => integ.keys.sort,
          "infra" => infra,
          "env_names" => env,
          "pricing_hints" => @pricing,
          "description" => @description,
          "tests" => { "files" => @test_files, "frameworks" => test_fw.keys.sort },
          "security" => @security
        }
      end
    end
  end
end
