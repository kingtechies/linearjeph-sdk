<?php
declare(strict_types=1);

namespace Linearjeph\Prophecy\Laravel;

use Closure;
use Linearjeph\Prophecy;
use Linearjeph\Prophecy\Routes;

/**
 * Laravel middleware. Register in bootstrap/app.php: ->withMiddleware(fn ($m) => $m->append(TrackRequests::class)),
 * or in app/Http/Kernel.php $middleware. Records route()->uri() (the template), method, status and duration.
 * No Laravel classes are imported, so the SDK has no framework dependency.
 */
final class TrackRequests
{
    /** @param \Illuminate\Http\Request $request */
    public function handle($request, Closure $next)
    {
        $start = hrtime(true);
        try {
            $response = $next($request);
        } catch (\Throwable $e) {
            $this->record($request, 500, $start);
            throw $e;
        }
        $this->record($request, is_object($response) && method_exists($response, 'getStatusCode') ? (int) $response->getStatusCode() : 200, $start);
        return $response;
    }

    private function record($request, int $status, int $start): void
    {
        try {
            $route = null;
            if (method_exists($request, 'route')) {
                $r = $request->route();
                if (is_object($r) && method_exists($r, 'uri')) {
                    $route = Routes::fromPattern((string) $r->uri());
                }
            }
            if ($route === null) {
                $route = Routes::normalize('/' . ltrim((string) $request->path(), '/'));
            }
            Prophecy::recordRequest($route, (string) $request->method(), $status, (hrtime(true) - $start) / 1e6);
        } catch (\Throwable) {
            // never into the host app
        }
    }
}
