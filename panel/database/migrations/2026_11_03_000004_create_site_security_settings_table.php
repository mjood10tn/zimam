<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/** Each site's security settings, with §9.6's defaults (§7.6). */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('site_security_settings', function (Blueprint $table) {
            $table->id();
            $table->foreignId('site_id')->unique()->constrained()->cascadeOnDelete();
            $table->boolean('force_https')->default(false);
            // Null is off; otherwise 86400, 2592000 or 31536000.
            $table->unsignedInteger('hsts_max_age')->nullable();
            $table->boolean('hsts_include_subdomains')->default(false);
            $table->boolean('headers_preset')->default(true);
            $table->boolean('block_risky_functions')->default(false);
            $table->boolean('restrict_to_site_folder')->default(false);
            $table->json('ip_allow');
            $table->json('ip_deny');
            $table->boolean('password_protection')->default(false);
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('site_security_settings');
    }
};
