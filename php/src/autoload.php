<?php
// Minimal PSR-4 autoloader for use without Composer: require 'path/to/src/autoload.php';
// The namespace prefix matches case-insensitively, so Linearjeph\ and LinearJeph\ both load.
spl_autoload_register(static function (string $class): void {
    $prefix = 'Linearjeph\\';
    if (strncasecmp($class, $prefix, strlen($prefix)) !== 0) {
        return;
    }
    $file = __DIR__ . '/' . str_replace('\\', '/', substr($class, strlen($prefix))) . '.php';
    if (is_file($file)) {
        require $file;
    }
});
