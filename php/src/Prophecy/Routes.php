<?php
declare(strict_types=1);

namespace Linearjeph\Prophecy;

final class Routes
{
    /** Turn a raw path into a route template: numeric, UUID and long hex segments become ":id"; the query string is dropped. */
    public static function normalize(string $path): string
    {
        $path = preg_split('~[?#]~', $path, 2)[0];
        if ($path === '') {
            return '/';
        }
        $segs = explode('/', $path);
        foreach ($segs as $i => $s) {
            if ($s !== '' && (ctype_digit($s)
                || preg_match('~^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$~i', $s)
                || preg_match('~^[0-9a-f]{16,}$~i', $s))) {
                $segs[$i] = ':id';
            }
        }
        return implode('/', $segs);
    }

    /** "users/{id}/orders/{order?}" (Laravel/Slim/FastRoute) -> "/users/:id/orders/:order". */
    public static function fromPattern(string $pattern): string
    {
        $p = preg_replace('~\{(\w+)(?::[^}]*)?\??\}~', ':$1', $pattern) ?? $pattern;
        return '/' . ltrim($p, '/');
    }
}
