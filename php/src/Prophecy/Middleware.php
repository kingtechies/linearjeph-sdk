<?php
declare(strict_types=1);

namespace Linearjeph\Prophecy;

use Linearjeph\Prophecy;

/**
 * PSR-15 style middleware without requiring any psr package: any framework that calls process($request, $handler)
 * (Slim, Mezzio, Laminas, ...) works. Records route template, method, status and duration only.
 * It also works as a Slim-style callable: $app->add(new Middleware()).
 */
final class Middleware
{
    public function __invoke($request, $handler)
    {
        return $this->process($request, $handler);
    }

    public function process($request, $handler)
    {
        $start = hrtime(true);
        try {
            $response = $handler->handle($request);
        } catch (\Throwable $e) {
            $this->record($request, 500, $start);
            throw $e;
        }
        $this->record($request, $this->statusOf($response), $start);
        return $response;
    }

    private function statusOf($response): int
    {
        try {
            return is_object($response) && method_exists($response, 'getStatusCode') ? (int) $response->getStatusCode() : 200;
        } catch (\Throwable) {
            return 200;
        }
    }

    private function record($request, int $status, int $start): void
    {
        try {
            $route = null;
            $method = 'GET';
            if (is_object($request)) {
                if (method_exists($request, 'getAttribute')) {
                    $r = $request->getAttribute('route');
                    if (is_object($r) && method_exists($r, 'getPattern')) {
                        $route = Routes::fromPattern((string) $r->getPattern());
                    }
                }
                if ($route === null && method_exists($request, 'getUri')) {
                    $route = Routes::normalize((string) $request->getUri()->getPath());
                }
                if (method_exists($request, 'getMethod')) {
                    $method = (string) $request->getMethod();
                }
            }
            Prophecy::recordRequest($route ?? '/', $method, $status, (hrtime(true) - $start) / 1e6);
        } catch (\Throwable) {
            // never into the host app
        }
    }
}
