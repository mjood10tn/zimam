<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The firewall rules Zimam owns (R06-D3), as firewall.status last reported
 * them: the owner's rules and the locked ones (SSH ports, http, https).
 * Rules Zimam didn't make are never stored.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('firewall_rules', function (Blueprint $table) {
            $table->id();
            $table->string('key', 24)->unique();
            $table->string('action', 8);
            $table->string('proto', 3);
            $table->unsignedInteger('port_from');
            $table->unsignedInteger('port_to');
            $table->json('from');
            $table->boolean('locked')->default(false);
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('firewall_rules');
    }
};
