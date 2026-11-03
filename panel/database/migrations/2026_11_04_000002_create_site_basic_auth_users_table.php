<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Each site's password-protection users (§7.6, §9.6): the bcrypt hash the
 * panel made, never the password. The agent writes them to htpasswd.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('site_basic_auth_users', function (Blueprint $table) {
            $table->id();
            $table->foreignId('site_id')->constrained()->cascadeOnDelete();
            $table->string('username', 32);
            $table->string('password_hash', 60);
            $table->timestamps();
            $table->unique(['site_id', 'username']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('site_basic_auth_users');
    }
};
