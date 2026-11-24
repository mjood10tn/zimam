<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Guarded changes (R06-D1): a firewall or SSH change the agent reverts by
 * itself unless the owner keeps it before its deadline. `params` is what was
 * asked, shown in the countdown and applied to the panel's copy on Keep.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('guarded_changes', function (Blueprint $table) {
            $table->id();
            $table->string('agent_change_id', 26)->unique();
            $table->string('kind', 16);
            $table->timestamp('deadline');
            $table->string('status', 16)->default('pending');
            $table->json('params');
            $table->foreignId('created_by')->nullable()->constrained('users')->nullOnDelete();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('guarded_changes');
    }
};
