<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/** The PHP versions the panel shows, kept in line with system.status (§7.6). */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('php_versions', function (Blueprint $table) {
            $table->id();
            $table->string('version')->unique();
            // installing, installed, uninstalling or failed.
            $table->string('status');
            $table->boolean('is_default')->default(false);
            $table->boolean('is_panel')->default(false);
            // A site or the panel has a pool on it, so it can't be uninstalled.
            $table->boolean('in_use')->default(false);
            // From the agent's allowlist (D19).
            $table->date('eol_date')->nullable();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('php_versions');
    }
};
