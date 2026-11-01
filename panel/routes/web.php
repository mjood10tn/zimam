<?php

use Illuminate\Support\Facades\Route;

// The panel lives under /admin.
Route::redirect('/', '/admin');
